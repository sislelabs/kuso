package kusoCli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso registry rebuild` is the recovery path for a lost in-cluster
// registry volume (BLOB-R6): every env pins an image only that registry
// held, so the next restart of any pod fails ImagePullBackOff. It rebuilds
// each env's deployed commit (or, when kuso no longer knows it, the env's
// branch head) and lets the normal promotion roll the env onto it.

var (
	registryRebuildAll         bool
	registryRebuildOnlyMissing bool
	registryRebuildPreviews    bool
	registryRebuildDryRun      bool
	registryRebuildYes         bool
)

type rebuildEnv struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Kind    string `json:"kind"`
		Runtime string `json:"runtime"`
		Image   *struct {
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		} `json:"image"`
	} `json:"spec"`
}

// rebuildTarget is one env whose live image lives in the in-cluster registry.
type rebuildTarget struct {
	Project, Service, Env, EnvCR, Tag string
}

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// inClusterImage mirrors registryauth.ForeignRepo's host test.
func inClusterImage(repo string) bool {
	host, _, ok := strings.Cut(strings.ToLower(repo), "/")
	if !ok {
		return false
	}
	host, _, _ = strings.Cut(host, ":")
	return host == "kuso-registry" || strings.HasPrefix(host, "kuso-registry.")
}

// rebuildTargets picks the envs of one project to rebuild. missing, when
// non-nil, limits the set to env CRs kuso health reports as missing.
func rebuildTargets(project string, envs []rebuildEnv, previews bool, missing map[string]bool) []rebuildTarget {
	var out []rebuildTarget
	for _, e := range envs {
		svc, env := e.Metadata.Labels["kuso.sislelabs.com/service"], e.Metadata.Labels["kuso.sislelabs.com/env"]
		switch {
		case svc == "" || env == "" || e.Spec.Image == nil || e.Spec.Image.Tag == "":
			continue
		case e.Spec.Runtime == "image" || !inClusterImage(e.Spec.Image.Repository):
			continue
		case !previews && (e.Spec.Kind == "preview" || strings.HasPrefix(env, "preview-")):
			continue
		case missing != nil && !missing[e.Metadata.Name]:
			continue
		}
		out = append(out, rebuildTarget{Project: project, Service: svc, Env: env, EnvCR: e.Metadata.Name, Tag: e.Spec.Image.Tag})
	}
	return out
}

// deployedCommit finds the full SHA of the build that produced tag.
func deployedCommit(builds []buildRow, tag string) string {
	for _, b := range builds {
		if b.ImageTag == tag && fullSHA.MatchString(b.CommitSha) {
			return b.CommitSha
		}
	}
	return ""
}

func missingImageEnvs() (map[string]bool, error) {
	resp, err := api.ReconcileHealth()
	if err := checkRespErr(resp, err); err != nil {
		return nil, fmt.Errorf("read kuso health: %w", err)
	}
	var rep healthReport
	if err := json.Unmarshal(resp.Body(), &rep); err != nil {
		return nil, fmt.Errorf("decode kuso health: %w", err)
	}
	out := map[string]bool{}
	for _, is := range rep.Issues {
		if is.Kind == "image_missing_from_registry" {
			out[is.Resource] = true
		}
	}
	return out, nil
}

var registryRebuildCmd = &cobra.Command{
	Use:   "rebuild [project...]",
	Short: "Rebuild the live images of every environment (recovery after losing the in-cluster registry)",
	Long: `Rebuild what each environment runs from the in-cluster registry, for
example after its volume was lost and pods fail ImagePullBackOff.

Each env is rebuilt from the commit it was deployed from when kuso still
has that build's record, otherwise from its branch head (printed as such).
Release hooks (migrations) run again on promotion. runtime=image services
are skipped: their images live in an external registry. Builds queue like
any other, so a large rebuild takes a while; follow it with 'kuso build list'.`,
	Example: `  kuso registry rebuild --all --only-missing --dry-run
  kuso registry rebuild shop blog -y`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if registryRebuildAll == (len(args) > 0) {
			return fmt.Errorf("name one or more projects, or pass --all")
		}
		projects := args
		if registryRebuildAll {
			resp, err := api.GetProjects()
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("fetch projects: %w", err)
			}
			projects = namesFromList(resp.Body())
		}
		var missing map[string]bool
		if registryRebuildOnlyMissing {
			var err error
			if missing, err = missingImageEnvs(); err != nil {
				return err
			}
		}
		var targets []rebuildTarget
		for _, p := range projects {
			resp, err := api.GetEnvironments(p)
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("list envs of %s: %w", p, err)
			}
			var envs []rebuildEnv
			if err := json.Unmarshal(resp.Body(), &envs); err != nil {
				return fmt.Errorf("decode envs of %s: %w", p, err)
			}
			targets = append(targets, rebuildTargets(p, envs, registryRebuildPreviews, missing)...)
		}
		if len(targets) == 0 {
			fmt.Println("nothing to rebuild")
			return nil
		}
		if !registryRebuildDryRun {
			if err := confirmDestructive(registryRebuildYes,
				fmt.Sprintf("Start %d builds and roll each env onto its rebuilt image?", len(targets))); err != nil {
				return err
			}
		}
		history := map[string][]buildRow{}
		var failed int
		for _, t := range targets {
			key := t.Project + "/" + t.Service
			if _, ok := history[key]; !ok {
				var rows []buildRow
				if resp, err := api.ListBuilds(t.Project, t.Service); checkRespErr(resp, err) == nil {
					_ = json.Unmarshal(resp.Body(), &rows)
				}
				history[key] = rows
			}
			req := kusoApi.CreateBuildRequest{Env: t.Env, Ref: deployedCommit(history[key], t.Tag)}
			from := "commit " + req.Ref
			if req.Ref == "" {
				from = "branch head (deployed commit unknown)"
			}
			if registryRebuildDryRun {
				fmt.Printf("would rebuild %s/%s env=%s from %s\n", t.Project, t.Service, t.Env, from)
				continue
			}
			resp, err := api.CreateBuild(t.Project, t.Service, req)
			if err := checkRespErr(resp, err); err != nil {
				failed++
				fmt.Printf("FAILED %s/%s env=%s: %v\n", t.Project, t.Service, t.Env, err)
				continue
			}
			fmt.Printf("rebuilding %s/%s env=%s from %s\n", t.Project, t.Service, t.Env, from)
		}
		if failed > 0 {
			return fmt.Errorf("%d of %d rebuilds could not be started", failed, len(targets))
		}
		return nil
	},
}

func init() {
	registryRebuildCmd.Flags().BoolVar(&registryRebuildAll, "all", false, "rebuild every project")
	registryRebuildCmd.Flags().BoolVar(&registryRebuildOnlyMissing, "only-missing", false, "only envs kuso health reports as missing their image")
	registryRebuildCmd.Flags().BoolVar(&registryRebuildPreviews, "previews", false, "include PR preview envs")
	registryRebuildCmd.Flags().BoolVar(&registryRebuildDryRun, "dry-run", false, "print the plan, start nothing")
	registryRebuildCmd.Flags().BoolVarP(&registryRebuildYes, "yes", "y", false, "skip the confirmation prompt")
	registryCmd.AddCommand(registryRebuildCmd)
}
