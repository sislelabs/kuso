// service_set_fields.go: the `kuso service set` flags for spec fields the
// web settings page edits but the CLI couldn't: run command, Dockerfile,
// release hook, pre-deploy snapshot, preview opt-out, build args, public
// env, static / buildpacks build config, image repo + tag and the HPA CPU
// target. Registered on both `kuso project service set` and the top-level
// `kuso service set` via addServiceSetFieldFlags; applied by
// applyServiceSetFields from the shared RunE in project.go.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var (
	serviceSetCommand           []string
	serviceSetDockerfile        string
	serviceSetReleaseCmd        string
	serviceSetReleaseTimeout    int
	serviceSetReleaseClear      bool
	serviceSetSnapshot          string // "on" | "off" | "" (leave alone)
	serviceSetPreviews          string // "on" | "off" | "" (leave alone)
	serviceSetBuildArgs         []string
	serviceSetBuildArgsClear    bool
	serviceSetPublicEnv         []string
	serviceSetPublicEnvClear    bool
	serviceSetStaticBuildCmd    string
	serviceSetStaticOutputDir   string
	serviceSetBuildpacksBuilder string
	serviceSetImageRepo         string
	serviceSetImageTag          string
	serviceSetTargetCPU         int
)

// addServiceSetFieldFlags registers the flags read by applyServiceSetFields.
// Both `service set` shells must call it: a flag missing from one shell is
// silently never Changed() there.
func addServiceSetFieldFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringSliceVar(&serviceSetCommand, "command", nil, "container argv, replaces the current one (repeat or comma-separate; '' clears)")
	f.StringVar(&serviceSetDockerfile, "dockerfile", "", "Dockerfile filename relative to the repo path (runtime=dockerfile; '' resets to Dockerfile)")
	f.StringVar(&serviceSetReleaseCmd, "release-cmd", "", "pre-deploy release hook (migrations), shell-quoted, e.g. 'npx prisma migrate deploy'")
	f.IntVar(&serviceSetReleaseTimeout, "release-timeout", 0, "release hook timeout in seconds (default 900)")
	f.BoolVar(&serviceSetReleaseClear, "release-clear", false, "remove the release hook")
	f.StringVar(&serviceSetSnapshot, "snapshot-before-deploy", "", "snapshot subscribed postgres addons before the release hook runs (on|off)")
	f.StringVar(&serviceSetPreviews, "previews", "", "PR previews for this service: off opts out, on follows the project setting (on|off)")
	f.StringArrayVar(&serviceSetBuildArgs, "build-arg", nil, "build-time KEY=VALUE, repeatable; replaces ALL build args (not for secrets)")
	f.BoolVar(&serviceSetBuildArgsClear, "build-args-clear", false, "remove every build arg")
	f.StringArrayVar(&serviceSetPublicEnv, "public-env", nil, "env var NAME baked as a placeholder at build and filled at pod start (NEXT_PUBLIC_*), repeatable; replaces the list")
	f.BoolVar(&serviceSetPublicEnvClear, "public-env-clear", false, "remove every public env name")
	f.StringVar(&serviceSetStaticBuildCmd, "static-build-cmd", "", "(runtime=static) build command, e.g. 'npm run build'")
	f.StringVar(&serviceSetStaticOutputDir, "static-output-dir", "", "(runtime=static) directory the build writes, e.g. dist")
	f.StringVar(&serviceSetBuildpacksBuilder, "buildpacks-builder", "", "(runtime=buildpacks) builder image ('' = Paketo default)")
	f.StringVar(&serviceSetImageRepo, "image-repo", "", "(runtime=image) registry image, e.g. ghcr.io/owner/app")
	f.StringVar(&serviceSetImageTag, "image-tag", "", "(runtime=image) image tag ('' = latest)")
	f.IntVar(&serviceSetTargetCPU, "target-cpu", 0, "autoscaler CPU target in percent of the request")
}

// currentServiceSpec is the subset of GET .../services/{s} that the
// read-modify-write flags need.
type currentServiceSpec struct {
	Spec struct {
		Release *struct {
			Command        []string `json:"command"`
			TimeoutSeconds int      `json:"timeoutSeconds"`
		} `json:"release"`
		Static     *kusoApi.ServiceStatic     `json:"static"`
		Buildpacks *kusoApi.ServiceBuildpacks `json:"buildpacks"`
		Image      *struct {
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		} `json:"image"`
		Placement *kusoApi.PatchPlacementRequest `json:"placement"`
		Volumes   []kusoApi.VolumePatch          `json:"volumes"`
	} `json:"spec"`
}

func fetchServiceSpec(project, service string) (*currentServiceSpec, error) {
	resp, err := api.GetService(project, service)
	if err := checkRespErr(resp, err); err != nil {
		return nil, fmt.Errorf("fetch current service spec: %w", err)
	}
	var cur currentServiceSpec
	if err := json.Unmarshal(resp.Body(), &cur); err != nil {
		return nil, fmt.Errorf("decode current service: %w", err)
	}
	return &cur, nil
}

// applyServiceSetFields adds the fields behind addServiceSetFieldFlags to
// req. getCur is called at most once, and only when a flag needs the
// current spec to avoid wiping a sibling field.
func applyServiceSetFields(cmd *cobra.Command, req *kusoApi.PatchServiceRequest, getCur func() (*currentServiceSpec, error)) error {
	changed := cmd.Flags().Changed
	var cur *currentServiceSpec
	current := func() (*currentServiceSpec, error) {
		if cur != nil {
			return cur, nil
		}
		c, err := getCur()
		if err != nil {
			return nil, err
		}
		cur = c
		return cur, nil
	}

	if changed("command") {
		argv := make([]string, 0, len(serviceSetCommand))
		for _, a := range serviceSetCommand {
			if a != "" {
				argv = append(argv, a)
			}
		}
		req.Command = &argv
	}
	if changed("dockerfile") {
		v := serviceSetDockerfile
		req.Dockerfile = &v
	}

	if serviceSetReleaseClear {
		if changed("release-cmd") || changed("release-timeout") {
			return fmt.Errorf("--release-clear can't be combined with --release-cmd / --release-timeout")
		}
		req.Release = &kusoApi.PatchReleaseRequest{Clear: true}
	} else if changed("release-cmd") || changed("release-timeout") {
		// The server replaces the hook wholesale (a missing timeout resets
		// to 900, a missing command DROPS the hook), so keep whichever half
		// the user didn't pass.
		rel := &kusoApi.PatchReleaseRequest{}
		if !changed("release-cmd") || !changed("release-timeout") {
			c, err := current()
			if err != nil {
				return err
			}
			if c.Spec.Release != nil {
				rel.Command = c.Spec.Release.Command
				rel.TimeoutSeconds = c.Spec.Release.TimeoutSeconds
			}
		}
		if changed("release-cmd") {
			rel.Command = splitCmd(serviceSetReleaseCmd)
			if len(rel.Command) == 0 {
				return fmt.Errorf("--release-cmd is empty; use --release-clear to remove the hook")
			}
		}
		if changed("release-timeout") {
			if serviceSetReleaseTimeout <= 0 {
				return fmt.Errorf("--release-timeout must be a positive number of seconds (got %d)", serviceSetReleaseTimeout)
			}
			rel.TimeoutSeconds = serviceSetReleaseTimeout
		}
		if len(rel.Command) == 0 {
			return fmt.Errorf("--release-timeout needs a release hook; this service has none, so pass --release-cmd too")
		}
		req.Release = rel
	}

	if changed("snapshot-before-deploy") {
		v, err := parseSetOnOff("snapshot-before-deploy", serviceSetSnapshot)
		if err != nil {
			return err
		}
		req.SnapshotBeforeDeploy = &v
	}
	if changed("previews") {
		on, err := parseSetOnOff("previews", serviceSetPreviews)
		if err != nil {
			return err
		}
		// {disabled:false} rather than {clear:true}: clear also wipes the
		// per-service preview seed / review settings.
		req.Previews = &kusoApi.PatchServicePreviewsRequest{Disabled: !on}
	}

	if serviceSetBuildArgsClear {
		if changed("build-arg") {
			return fmt.Errorf("--build-args-clear can't be combined with --build-arg")
		}
		m := map[string]string{}
		req.BuildArgs = &m
	} else if changed("build-arg") {
		m, err := parseBuildArgs(serviceSetBuildArgs)
		if err != nil {
			return err
		}
		req.BuildArgs = &m
	}
	if serviceSetPublicEnvClear {
		if changed("public-env") {
			return fmt.Errorf("--public-env-clear can't be combined with --public-env")
		}
		l := []string{}
		req.PublicEnv = &l
	} else if changed("public-env") {
		l := make([]string, 0, len(serviceSetPublicEnv))
		for _, n := range serviceSetPublicEnv {
			if n = strings.TrimSpace(n); n != "" {
				l = append(l, n)
			}
		}
		if len(l) == 0 {
			return fmt.Errorf("--public-env is empty; use --public-env-clear to remove the list")
		}
		req.PublicEnv = &l
	}

	// static / buildpacks / image are replaced wholesale server-side, so
	// start from the stored block and overlay the flags.
	if changed("static-build-cmd") || changed("static-output-dir") {
		c, err := current()
		if err != nil {
			return err
		}
		st := kusoApi.ServiceStatic{}
		if c.Spec.Static != nil {
			st = *c.Spec.Static
		}
		if changed("static-build-cmd") {
			st.BuildCmd = serviceSetStaticBuildCmd
		}
		if changed("static-output-dir") {
			st.OutputDir = serviceSetStaticOutputDir
		}
		req.Static = &st
	}
	if changed("buildpacks-builder") {
		c, err := current()
		if err != nil {
			return err
		}
		bp := kusoApi.ServiceBuildpacks{}
		if c.Spec.Buildpacks != nil {
			bp = *c.Spec.Buildpacks
		}
		bp.BuilderImage = serviceSetBuildpacksBuilder
		req.Buildpacks = &bp
	}
	if changed("image-repo") || changed("image-tag") {
		c, err := current()
		if err != nil {
			return err
		}
		img := &kusoApi.PatchImageRequest{}
		if c.Spec.Image != nil {
			img.Repository, img.Tag = c.Spec.Image.Repository, c.Spec.Image.Tag
		}
		if req.Image != nil {
			// --image-pull-secret was set too; keep its value.
			img.PullSecret = req.Image.PullSecret
		}
		if changed("image-repo") {
			img.Repository = strings.TrimSpace(serviceSetImageRepo)
			if img.Repository == "" {
				return fmt.Errorf("--image-repo is empty; switch the runtime instead of clearing the image")
			}
		}
		if changed("image-tag") {
			img.Tag = strings.TrimSpace(serviceSetImageTag)
		}
		if img.Repository == "" {
			return fmt.Errorf("--image-tag needs an image; this service has none, so pass --image-repo too")
		}
		req.Image = img
	}

	if changed("target-cpu") {
		if req.Scale == nil {
			req.Scale = &kusoApi.PatchScaleRequest{}
		}
		v := serviceSetTargetCPU
		req.Scale.TargetCPU = &v
	}
	return nil
}

// parseSetOnOff reads an on|off flag value.
func parseSetOnOff(flag, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes":
		return true, nil
	case "off", "false", "no":
		return false, nil
	}
	return false, fmt.Errorf("--%s must be on|off (got %q)", flag, v)
}

// parseBuildArgs turns repeated KEY=VALUE flags into a map. The server
// rejects non-identifier keys; a duplicate key here is a user mistake.
func parseBuildArgs(kvs []string) (map[string]string, error) {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("--build-arg %q is not KEY=VALUE", kv)
		}
		if _, dup := m[k]; dup {
			return nil, fmt.Errorf("--build-arg %s given twice", k)
		}
		m[k] = v
	}
	return m, nil
}

// `service add` counterparts of the build/deploy flags above. Separate
// vars from `service set`: different commands, and add has nothing to
// read-modify-write.
var (
	serviceAddReleaseCmd        string
	serviceAddReleaseTimeout    int
	serviceAddSnapshot          string
	serviceAddBuildArgs         []string
	serviceAddPublicEnv         []string
	serviceAddStaticBuildCmd    string
	serviceAddStaticOutputDir   string
	serviceAddBuildpacksBuilder string
)

func addServiceAddFieldFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&serviceAddReleaseCmd, "release-cmd", "", "pre-deploy release hook (migrations), shell-quoted, e.g. 'npx prisma migrate deploy'")
	f.IntVar(&serviceAddReleaseTimeout, "release-timeout", 0, "release hook timeout in seconds (default 900)")
	f.StringVar(&serviceAddSnapshot, "snapshot-before-deploy", "", "snapshot subscribed postgres addons before the release hook runs (on|off)")
	f.StringArrayVar(&serviceAddBuildArgs, "build-arg", nil, "build-time KEY=VALUE, repeatable (not for secrets)")
	f.StringArrayVar(&serviceAddPublicEnv, "public-env", nil, "env var NAME baked as a placeholder at build and filled at pod start (NEXT_PUBLIC_*), repeatable")
	f.StringVar(&serviceAddStaticBuildCmd, "static-build-cmd", "", "(runtime=static) build command, e.g. 'npm run build'")
	f.StringVar(&serviceAddStaticOutputDir, "static-output-dir", "", "(runtime=static) directory the build writes, e.g. dist")
	f.StringVar(&serviceAddBuildpacksBuilder, "buildpacks-builder", "", "(runtime=buildpacks) builder image ('' = Paketo default)")
}

func applyServiceAddFields(cmd *cobra.Command, req *kusoApi.CreateServiceRequest) error {
	changed := cmd.Flags().Changed
	if changed("release-cmd") {
		argv := splitCmd(serviceAddReleaseCmd)
		if len(argv) == 0 {
			return fmt.Errorf("--release-cmd is empty")
		}
		req.Release = &kusoApi.ServiceRelease{Command: argv}
	}
	if changed("release-timeout") {
		if req.Release == nil {
			return fmt.Errorf("--release-timeout needs --release-cmd")
		}
		if serviceAddReleaseTimeout <= 0 {
			return fmt.Errorf("--release-timeout must be a positive number of seconds (got %d)", serviceAddReleaseTimeout)
		}
		req.Release.TimeoutSeconds = serviceAddReleaseTimeout
	}
	if changed("snapshot-before-deploy") {
		v, err := parseSetOnOff("snapshot-before-deploy", serviceAddSnapshot)
		if err != nil {
			return err
		}
		req.SnapshotBeforeDeploy = &v
	}
	if changed("build-arg") {
		m, err := parseBuildArgs(serviceAddBuildArgs)
		if err != nil {
			return err
		}
		req.BuildArgs = m
	}
	for _, n := range serviceAddPublicEnv {
		if n = strings.TrimSpace(n); n != "" {
			req.PublicEnv = append(req.PublicEnv, n)
		}
	}
	if changed("static-build-cmd") || changed("static-output-dir") {
		req.Static = &kusoApi.ServiceStatic{BuildCmd: serviceAddStaticBuildCmd, OutputDir: serviceAddStaticOutputDir}
	}
	if changed("buildpacks-builder") {
		req.Buildpacks = &kusoApi.ServiceBuildpacks{BuilderImage: serviceAddBuildpacksBuilder}
	}
	return nil
}
