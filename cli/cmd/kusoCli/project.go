package kusoCli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"kuso/pkg/kusoApi"
)

// `kuso project` covers project lifecycle. Mirrors the v0.2 server REST
// surface (POST /api/projects, DELETE /api/projects/:p, etc.) and stays
// agent-friendly: every flag is required, no interactive survey prompts.

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage projects (create, delete, describe)",
}

// ---------------- project create ----------------

var (
	projectCreateRepo           string
	projectCreateBranch         string
	projectCreateDomain         string
	projectCreatePreviews       bool
	projectCreateInstallationID int64
	projectCreateNamespace      string
)

var projectCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new project",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso project create analiz --repo https://github.com/sislelabs/analiz
  kuso project create analiz --repo ... --branch main --previews --domain analiz.example.com`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if projectCreateRepo == "" {
			return fmt.Errorf("--repo is required")
		}
		req := kusoApi.CreateProjectRequest{
			Name:       args[0],
			BaseDomain: projectCreateDomain,
			Namespace:  projectCreateNamespace,
			DefaultRepo: &kusoApi.RepoRef{
				URL:           projectCreateRepo,
				DefaultBranch: projectCreateBranch,
			},
			Previews: &kusoApi.PreviewsSettings{Enabled: projectCreatePreviews},
		}
		if projectCreateInstallationID > 0 {
			req.GitHub = &kusoApi.GitHubInstallationRef{InstallationID: projectCreateInstallationID}
		}
		resp, err := api.CreateProject(req)
		if err != nil {
			return fmt.Errorf("create: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("project %s created\n", args[0])
		return nil
	},
}

// ---------------- project update ----------------

var (
	projectUpdateBranch         string
	projectUpdateRepo           string
	projectUpdateDomain         string
	projectUpdateDescription    string
	projectUpdateInstallation   int64
	projectUpdateInstallReset   bool
	projectUpdatePreviews       string // "on" | "off" | "" (leave alone)
	projectUpdatePreviewsTTL    int
	projectUpdatePreviewsDomain string // base domain for preview hosts, "" = leave alone
	projectUpdateAlwaysOn       string // "on" | "off" | "" (leave alone)
)

var projectUpdateCmd = &cobra.Command{
	Use:   "update <name>",
	Short: "Patch a project's spec (defaults left alone unless flagged)",
	Long: `Update specific fields on a project. Only the flags you pass are
changed; everything else is left as-is. To explicitly disable previews
use --previews=off; to enable, --previews=on. To detach a GitHub App
installation use --github-installation-clear (sets installationId to 0).`,
	Args: cobra.ExactArgs(1),
	Example: `  kuso project update analiz --previews=on
  kuso project update analiz --branch develop --domain analiz.example.com
  kuso project update analiz --github-installation 128668920
  kuso project update analiz --github-installation-clear`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req := kusoApi.UpdateProjectRequest{}
		if cmd.Flags().Changed("description") {
			req.Description = kusoApi.StringPtr(projectUpdateDescription)
		}
		if cmd.Flags().Changed("domain") {
			req.BaseDomain = kusoApi.StringPtr(projectUpdateDomain)
		}
		if cmd.Flags().Changed("repo") || cmd.Flags().Changed("branch") {
			req.DefaultRepo = &kusoApi.RepoRef{
				URL:           projectUpdateRepo,
				DefaultBranch: projectUpdateBranch,
			}
		}
		if projectUpdateInstallReset {
			req.GitHub = &kusoApi.GitHubInstallationRef{InstallationID: 0}
		} else if cmd.Flags().Changed("github-installation") {
			req.GitHub = &kusoApi.GitHubInstallationRef{InstallationID: projectUpdateInstallation}
		}
		if cmd.Flags().Changed("previews") || cmd.Flags().Changed("previews-ttl") || cmd.Flags().Changed("previews-domain") {
			pv := &kusoApi.PreviewsPatch{}
			switch projectUpdatePreviews {
			case "on", "true", "yes":
				pv.Enabled = kusoApi.BoolPtr(true)
			case "off", "false", "no":
				pv.Enabled = kusoApi.BoolPtr(false)
			case "":
				// leave alone
			default:
				return fmt.Errorf("--previews must be on|off (got %q)", projectUpdatePreviews)
			}
			if cmd.Flags().Changed("previews-ttl") {
				pv.TTLDays = kusoApi.IntPtr(projectUpdatePreviewsTTL)
			}
			if cmd.Flags().Changed("previews-domain") {
				pv.BaseDomain = kusoApi.StringPtr(projectUpdatePreviewsDomain)
			}
			req.Previews = pv
		}
		if cmd.Flags().Changed("always-on") {
			switch projectUpdateAlwaysOn {
			case "on", "true", "yes":
				req.AlwaysOn = kusoApi.BoolPtr(true)
			case "off", "false", "no":
				req.AlwaysOn = kusoApi.BoolPtr(false)
			default:
				return fmt.Errorf("--always-on must be on|off (got %q)", projectUpdateAlwaysOn)
			}
		}
		resp, err := api.UpdateProject(args[0], req)
		if err != nil {
			return fmt.Errorf("update: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("project %s updated\n", args[0])
		return nil
	},
}

// ---------------- project delete ----------------

var (
	projectDeleteYes       bool
	projectDeletePurgeData bool
)

var projectDeleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a project (cascades to services, envs, addons; PVCs are KEPT unless --purge-data)",
	Long: `Delete a kuso project and every owned resource (services, envs,
addons, builds, secrets).

Addon PVCs are KEPT by default. The helm-operator stamps
"helm.sh/resource-policy: keep" on every addon PVC so an accidental
project delete doesn't turn into accidental data loss. Pass
--purge-data to also wipe the PVCs — required when you actually
want a clean slate. Without it, a delete+recreate cycle inherits
the OLD postgres data dir AND the OLD password from disk, while
the new addon spec generates a new password, and pods crashloop
with SASL auth failures.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		msg := fmt.Sprintf("Delete project %q (cascades to services, envs, addons)?", args[0])
		if projectDeletePurgeData {
			msg = fmt.Sprintf("Delete project %q AND PURGE ALL DATA (PVCs)? This is irreversible.", args[0])
		}
		if err := confirmDestructive(projectDeleteYes, msg); err != nil {
			return err
		}
		resp, err := api.DeleteProjectOpts(args[0], kusoApi.DeleteProjectOptions{PurgeData: projectDeletePurgeData})
		if err != nil {
			return fmt.Errorf("delete: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		suffix := ""
		if projectDeletePurgeData {
			suffix = " (PVCs purged)"
		}
		fmt.Printf("project %s deleted%s\n", args[0], suffix)
		return nil
	},
}

// ---------------- project describe ----------------

var projectDescribeCmd = &cobra.Command{
	Use:     "describe <name>",
	Aliases: []string{"get"},
	Short:   "Show a project with its services, environments, and addons",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetProject(args[0])
		if err != nil {
			return fmt.Errorf("get: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var data map[string]any
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(data)
		default:
			project := mapAt(data, "project")
			spec := mapAt(project, "spec")
			repo := mapAt(spec, "defaultRepo")
			previews := mapAt(spec, "previews")
			fmt.Printf("project: %s\n", asString(mapAt(project, "metadata")["name"]))
			fmt.Printf("  repo:      %s\n", asString(repo["url"]))
			fmt.Printf("  branch:    %s\n", asString(repo["defaultBranch"]))
			fmt.Printf("  base:      %s\n", asString(spec["baseDomain"]))
			fmt.Printf("  previews:  %s\n", boolText(previews["enabled"]))

			services, _ := data["services"].([]any)
			envs, _ := data["environments"].([]any)
			addons, _ := data["addons"].([]any)
			fmt.Printf("services (%d), environments (%d), addons (%d)\n",
				len(services), len(envs), len(addons))
			return nil
		}
	},
}

// ---------------- project service add ----------------

var (
	serviceAddPath        string
	serviceAddRuntime     string
	serviceAddDockerfile  string
	serviceAddPort        int
	serviceAddImageRepo   string
	serviceAddImageTag    string
	serviceAddFromService string
	serviceAddCommand     []string
	// --replicas: HPA min. 0 = use chart default (1). Setting this to
	// 2 is the cheapest way to get rolling updates without a request
	// gap, and the cheapest way to survive a node drain.
	serviceAddMinReplicas int
	serviceAddMaxReplicas int
)

var projectServiceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage services within a project",
}

// serviceCmd is the top-level alias for `kuso project service ...`.
// Lets users type `kuso service add <project> <name>` directly, mirroring
// `kuso project create`. The subcommands themselves are shared with
// projectServiceCmd via init() — same RunE, same flags, same examples.
var serviceCmd = &cobra.Command{
	Use:     "service",
	Aliases: []string{"svc"},
	Short:   "Manage services (alias for `kuso project service`)",
}

// runServiceAdd is shared by both `kuso project service add` and the
// top-level `kuso service add` aliases. Same flags, same behavior.
var runServiceAdd = func(cmd *cobra.Command, args []string) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	req := kusoApi.CreateServiceRequest{
		Name:       args[1],
		Runtime:    serviceAddRuntime,
		Dockerfile: serviceAddDockerfile,
		Port:       int32(serviceAddPort),
		Repo:       &kusoApi.ServiceRepoSpec{Path: serviceAddPath},
		Command: serviceAddCommand,
	}
	// Build a ServiceScale only when the user actually set a flag —
	// passing an all-zero struct would clobber the chart's defaults
	// (Min=1, Max=5) with explicit 0s, which the operator interprets
	// as "scale to zero", which is almost never what `--replicas 0`
	// would mean to a user typing the flag.
	if serviceAddMinReplicas > 0 || serviceAddMaxReplicas > 0 {
		req.Scale = &kusoApi.ServiceScale{
			Min: serviceAddMinReplicas,
			Max: serviceAddMaxReplicas,
		}
		// If only min is set, default max to min so the autoscaler
		// doesn't render a Max < Min and refuse to schedule. Mirrors
		// what `kuso service set` does on a partial update.
		if serviceAddMaxReplicas == 0 {
			req.Scale.Max = serviceAddMinReplicas
		}
	}
	// runtime=image: point kuso at a pre-built image instead of a
	// repo. The server requires image.repository when runtime is
	// "image" and rejects the request otherwise; the CLI mirrors
	// that check so the failure is friendly.
	if serviceAddRuntime == "image" {
		if serviceAddImageRepo == "" {
			return fmt.Errorf("--runtime=image requires --image-repo (e.g. ghcr.io/owner/app)")
		}
		req.Image = &kusoApi.ServiceImageSpec{
			Repository: serviceAddImageRepo,
			Tag:        serviceAddImageTag,
		}
	} else if serviceAddImageRepo != "" || serviceAddImageTag != "" {
		return fmt.Errorf("--image-repo / --image-tag only valid with --runtime=image")
	}
	// runtime=worker: no repo of its own, inherits the sibling's
	// image. Friendly client-side check so the user sees a clear
	// message; the server enforces the same constraint server-side.
	if serviceAddRuntime == "worker" {
		if serviceAddFromService == "" {
			return fmt.Errorf("--runtime=worker requires --from-service (the sibling service whose image to reuse)")
		}
		req.FromService = serviceAddFromService
		// Workers don't build independently. Drop the default "."
		// repo path so the server-side spec stays clean (the worker
		// CR has no Repo block at all).
		req.Repo = nil
		if len(serviceAddCommand) == 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: --runtime=worker without --command runs the image's default ENTRYPOINT; usually you want --command ./worker (or similar)\n")
		}
	} else if serviceAddFromService != "" {
		return fmt.Errorf("--from-service only valid with --runtime=worker (got runtime=%q)", serviceAddRuntime)
	}
	resp, err := api.AddService(args[0], req)
	if err != nil {
		return fmt.Errorf("add service: %w", err)
	}
	if resp.StatusCode() >= 300 {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
	}
	fmt.Printf("service %s/%s added\n", args[0], args[1])
	return nil
}

var serviceAddCmd = &cobra.Command{
	Use:   "add <project> <name>",
	Short: "Add a service to a project (creates production env automatically)",
	Args:  cobra.ExactArgs(2),
	Example: `  kuso project service add analiz api --port 8080
  kuso project service add analiz web --path apps/web --port 3000
  # Force a Dockerfile build instead of auto-detection:
  kuso service add analiz api --runtime dockerfile --port 8080`,
	RunE: runServiceAdd,
}

// Top-level alias commands. Cobra requires distinct *cobra.Command per
// parent (it sets cmd.parent on AddCommand), so we mirror the few
// service subcommands as thin shells that share runE closures + flag
// vars with the project-scoped originals.
var serviceAddTopCmd = &cobra.Command{
	Use:   "add <project> <name>",
	Short: "Add a service to a project (alias for `kuso project service add`)",
	Args:  cobra.ExactArgs(2),
	RunE:  runServiceAdd,
}

var serviceDeleteYes bool

var serviceDeleteCmd = &cobra.Command{
	Use:     "delete <project> <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a service",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(serviceDeleteYes,
			fmt.Sprintf("Delete service %s/%s?", args[0], args[1])); err != nil {
			return err
		}
		resp, err := api.DeleteService(args[0], args[1])
		if err != nil {
			return fmt.Errorf("delete service: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("service %s/%s deleted\n", args[0], args[1])
		return nil
	},
}

// ---------------- project service rename ----------------

var serviceRenameYes bool

var serviceRenameCmd = &cobra.Command{
	Use:   "rename <project> <old> <new>",
	Short: "Rename a service (clone-then-delete; brief downtime)",
	Long: `Rename a service. Implemented as clone-then-delete because kube
resource names are immutable, so the operation has real cost:

  - the new KusoService + KusoEnvironment CRs come up first, the old
    ones come down second; production traffic to the old hostname
    returns 503 for the seconds in between
  - DNS the rest of your services use to reference this one (e.g.
    ${{api.URL}}) re-resolves on the next save — restart consumers
    or re-set their env vars to pick up the new name
  - in-flight builds keyed on the old name finish but don't promote
  - preview envs are dropped (they regenerate on the next PR event)`,
	Example: `  kuso project service rename analiz api web
  kuso project service rename analiz worker queue --yes`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, oldName, newName := args[0], args[1], args[2]
		if oldName == newName {
			return fmt.Errorf("old and new names are the same")
		}
		if err := confirmDestructive(serviceRenameYes,
			fmt.Sprintf("Rename %s/%s → %s/%s? This causes brief downtime.",
				project, oldName, project, newName)); err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]string{"newName": newName})
		resp, err := api.RawPost(
			fmt.Sprintf("/api/projects/%s/services/%s/rename",
				url.PathEscape(project), url.PathEscape(oldName)),
			body, "application/json")
		if err != nil {
			return fmt.Errorf("rename service: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("service renamed: %s/%s → %s/%s\n", project, oldName, project, newName)
		return nil
	},
}

// ---------------- project service set ----------------

var (
	serviceSetDisplayName   string
	serviceSetPort          int32
	serviceSetRuntime       string
	serviceSetDomains       string // comma- or newline-separated host list
	serviceSetInternal      string // "on" | "off" | "" (leave alone)
	serviceSetPrivateEgress string // "on" | "off" | "" (leave alone)
	serviceSetMinReplicas   int
	serviceSetMaxReplicas   int
	serviceSetPath          string   // monorepo subpath (relative to repo root)
	serviceSetBranch        string   // git branch override
	serviceSetCapAdd        []string // Linux capabilities to add back (e.g. SETUID,SETGID)
	serviceSetAllowPrivEsc  string   // "on" | "off" | "" (leave alone)
)

var serviceSetCmd = &cobra.Command{
	Use:   "set <project> <service>",
	Short: "Edit a service's spec (display name / port / runtime / domains / visibility)",
	Long: `Patch fields on an existing service. Only the flags you pass are
changed; everything else is left as-is. Mirrors the UI's
Settings → Source / Networking flow.

  --display-name "Todo API"     # canvas + overlay label
  --port 8080                   # container port
  --runtime nixpacks            # build runtime
  --domains api.example.com,alt.example.com  # custom domains (replaces list)
  --internal=on                 # skip public Ingress (in-cluster only)
  --internal=off                # re-expose publicly
  --private-egress=on           # deny public internet egress
  --private-egress=off          # allow public internet egress
  --cap-add SETUID --cap-add SETGID   # add back Linux capabilities (repeatable)
  --allow-privilege-escalation=on     # allow a process to gain more privs than its parent
  --allow-privilege-escalation=off    # disallow (kuso's hardened default)`,
	Example: `  kuso project service set hui kuso-demo-todo-web --display-name "Todo Web"
  kuso project service set hui kuso-demo-todo-api --port 8080
  kuso project service set hui worker --internal=on
  kuso project service set hui kuso-demo-todo-web --domains mudo.sislelabs.com`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req := kusoApi.PatchServiceRequest{}
		if cmd.Flags().Changed("display-name") {
			v := serviceSetDisplayName
			req.DisplayName = &v
		}
		if cmd.Flags().Changed("port") {
			v := serviceSetPort
			req.Port = &v
		}
		if cmd.Flags().Changed("runtime") {
			v := serviceSetRuntime
			req.Runtime = &v
		}
		if cmd.Flags().Changed("domains") {
			// Split on comma OR whitespace so the user can paste either
			// "a.com,b.com" or "a.com b.com" or a newline-separated
			// list. Empty entries are dropped.
			parts := strings.FieldsFunc(serviceSetDomains, func(r rune) bool {
				return r == ',' || r == ' ' || r == '\n' || r == '\t'
			})
			doms := make([]kusoApi.PatchServiceDomain, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				doms = append(doms, kusoApi.PatchServiceDomain{Host: p, TLS: true})
			}
			// Empty list = clear all custom domains; non-empty = replace.
			req.Domains = &doms
		}
		if cmd.Flags().Changed("internal") {
			switch serviceSetInternal {
			case "on", "true", "yes":
				req.Internal = kusoApi.BoolPtr(true)
			case "off", "false", "no":
				req.Internal = kusoApi.BoolPtr(false)
			default:
				return fmt.Errorf("--internal must be on|off (got %q)", serviceSetInternal)
			}
		}
		if cmd.Flags().Changed("private-egress") {
			switch serviceSetPrivateEgress {
			case "on", "true", "yes":
				req.PrivateEgress = kusoApi.BoolPtr(true)
			case "off", "false", "no":
				req.PrivateEgress = kusoApi.BoolPtr(false)
			default:
				return fmt.Errorf("--private-egress must be on|off (got %q)", serviceSetPrivateEgress)
			}
		}
		if cmd.Flags().Changed("replicas") || cmd.Flags().Changed("max-replicas") {
			scale := &kusoApi.PatchScaleRequest{}
			if cmd.Flags().Changed("replicas") {
				v := serviceSetMinReplicas
				scale.Min = &v
			}
			if cmd.Flags().Changed("max-replicas") {
				v := serviceSetMaxReplicas
				scale.Max = &v
			}
			req.Scale = scale
		}
		if cmd.Flags().Changed("path") || cmd.Flags().Changed("branch") {
			// Server-side semantic: PatchRepoRequest with empty URL
			// CLEARS the repo block entirely (intentional, for the
			// "this is the new source" use case). So changing just
			// path/branch has to round-trip the existing URL through
			// a fetch-then-patch sequence — otherwise we'd silently
			// nuke the service's connection to its source repo.
			cur, err := api.GetService(args[0], args[1])
			if err != nil {
				return fmt.Errorf("fetch current service spec: %w", err)
			}
			if cur.StatusCode() >= 300 {
				return fmt.Errorf("fetch current service spec: server %d", cur.StatusCode())
			}
			var curWire struct {
				Spec struct {
					Repo *struct {
						URL           string `json:"url"`
						DefaultBranch string `json:"defaultBranch"`
						Path          string `json:"path"`
					} `json:"repo"`
				} `json:"spec"`
			}
			if err := json.Unmarshal(cur.Body(), &curWire); err != nil {
				return fmt.Errorf("decode current service: %w", err)
			}
			rp := &kusoApi.PatchRepoRequest{}
			if curWire.Spec.Repo != nil {
				rp.URL = curWire.Spec.Repo.URL
				rp.Branch = curWire.Spec.Repo.DefaultBranch
				rp.Path = curWire.Spec.Repo.Path
			}
			if rp.URL == "" {
				return fmt.Errorf("--path/--branch require an existing repo URL on the service; use `kuso service add` to set one")
			}
			if cmd.Flags().Changed("path") {
				rp.Path = serviceSetPath
			}
			if cmd.Flags().Changed("branch") {
				rp.Branch = serviceSetBranch
			}
			req.Repo = rp
		}
		if cmd.Flags().Changed("cap-add") || cmd.Flags().Changed("allow-privilege-escalation") {
			sc := &kusoApi.PatchSecurityContextRequest{}
			if cmd.Flags().Changed("cap-add") {
				caps := make([]string, 0, len(serviceSetCapAdd))
				for _, c := range serviceSetCapAdd {
					c = strings.TrimSpace(c)
					if c != "" {
						caps = append(caps, c)
					}
				}
				sc.Capabilities = &kusoApi.PatchCapabilitiesRequest{Add: caps}
			}
			if cmd.Flags().Changed("allow-privilege-escalation") {
				switch serviceSetAllowPrivEsc {
				case "on", "true", "yes":
					sc.AllowPrivilegeEscalation = kusoApi.BoolPtr(true)
				case "off", "false", "no":
					sc.AllowPrivilegeEscalation = kusoApi.BoolPtr(false)
				default:
					return fmt.Errorf("--allow-privilege-escalation must be on|off (got %q)", serviceSetAllowPrivEsc)
				}
			}
			req.SecurityContext = sc
		}
		resp, err := api.PatchService(args[0], args[1], req)
		if err != nil {
			return fmt.Errorf("patch service: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("service %s/%s updated\n", args[0], args[1])
		return nil
	},
}

// ---------------- project addon add ----------------

var (
	addonAddKind    string
	addonAddVersion string
	addonAddSize    string
	addonAddHA      bool
)

var supportedAddonKinds = []string{
	// Implemented kinds — chart renders real workloads + conn secret.
	"postgres", "redis", "s3",
	"mailpit", "nats", "meilisearch", "clickhouse", "redpanda",
	// Reserved (chart emits an "unsupported" marker); listed so the
	// CLI accepts the kind for projects that pre-declare the field.
	"mongodb", "mysql", "rabbitmq", "memcached",
	"elasticsearch", "kafka", "cockroachdb", "couchdb",
}

var projectAddonCmd = &cobra.Command{
	Use:   "addon",
	Short: "Manage addons within a project",
}

var addonAddCmd = &cobra.Command{
	Use:   "add <project> <name>",
	Short: "Add an addon to a project (auto-injected as envFrom into every service)",
	Args:  cobra.ExactArgs(2),
	Example: `  kuso project addon add analiz pg --kind postgres --version 16
  kuso project addon add analiz cache --kind redis --version 7 --size small`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if !contains(supportedAddonKinds, addonAddKind) {
			return fmt.Errorf("--kind must be one of: %s", strings.Join(supportedAddonKinds, ", "))
		}
		// Loud warnings for known-fragile single-pod defaults. These
		// addons are easy to provision but lose data or messages on
		// pod loss, and the failure mode is silent until something
		// dies. Surfacing it at add-time gives the operator one chance
		// to add --ha before any app starts depending on the addon.
		// stderr (not stdout) so scripts that parse `addon add` output
		// stay clean.
		if !addonAddHA {
			switch addonAddKind {
			case "nats":
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: NATS addon defaulting to single pod — pod loss or node failure DROPS in-flight JetStream messages")
				fmt.Fprintln(cmd.ErrOrStderr(), "         payment / ticketing / billing workloads should pass --ha for a 3-replica clustered StatefulSet")
				fmt.Fprintln(cmd.ErrOrStderr(), "         (even with --ha, streams must be created with `--replicas 3` for HA writes — see docs/ADDON_HA.md)")
			case "redis":
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: Redis addon defaulting to single pod — pod loss = ~30-60s of failed reads/writes until reschedule")
				fmt.Fprintln(cmd.ErrOrStderr(), "         apps using Redis for session state, rate limiting, or seat-hold counters should pass --ha (3 Redis + 3 Sentinel)")
			case "postgres":
				fmt.Fprintln(cmd.ErrOrStderr(), "note: Postgres addon defaulting to single pod — pass --ha for a 3-replica CloudNativePG Cluster with ~30s automatic failover")
				fmt.Fprintln(cmd.ErrOrStderr(), "      (requires cert-manager + the CNPG operator preinstalled — see docs/ADDON_HA.md)")
			}
		}
		req := kusoApi.CreateAddonRequest{
			Name:    args[1],
			Kind:    addonAddKind,
			Version: addonAddVersion,
			Size:    addonAddSize,
			HA:      addonAddHA,
		}
		resp, err := api.AddAddon(args[0], req)
		if err != nil {
			return fmt.Errorf("add addon: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("addon %s/%s (%s) added\n", args[0], args[1], addonAddKind)
		return nil
	},
}

// subscribableAddonsShape mirrors the GET
// /services/{service}/subscribed-addons response.
type subscribableAddonsShape struct {
	Subscribed []string `json:"subscribed"`
	Available  []string `json:"available"`
}

func readSubscribedAddons(project, service string) (*subscribableAddonsShape, error) {
	resp, err := api.GetSubscribedAddons(project, service)
	if err != nil {
		return nil, fmt.Errorf("read subscribed addons: %w", err)
	}
	if resp.StatusCode() >= 300 {
		return nil, fmt.Errorf("read subscribed addons: server returned %d: %s",
			resp.StatusCode(), string(resp.Body()))
	}
	var out subscribableAddonsShape
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("decode subscribed addons: %w", err)
	}
	return &out, nil
}

var addonListCmd = &cobra.Command{
	Use:   "list <project> <service>",
	Short: "List which addons a service is subscribed to (and which it could be)",
	Long: `Show a service's addon subscriptions. Only subscribed addons have their
connection secret (DATABASE_URL, REDIS_URL, …) injected into the service's pods.

Examples:
  kuso project addon list myproj web
  kuso project addon subscribe myproj web cache      # mount the redis addon's conn
  kuso project addon unsubscribe myproj web cache`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		sub, err := readSubscribedAddons(args[0], args[1])
		if err != nil {
			return err
		}
		subSet := map[string]bool{}
		for _, a := range sub.Subscribed {
			subSet[a] = true
		}
		all := append([]string{}, sub.Available...)
		sort.Strings(all)
		if len(all) == 0 {
			fmt.Printf("%s/%s: no project addons available to subscribe to\n", args[0], args[1])
			return nil
		}
		fmt.Printf("addons for %s/%s (✓ = subscribed, conn injected):\n", args[0], args[1])
		for _, a := range all {
			mark := " "
			if subSet[a] {
				mark = "✓"
			}
			fmt.Printf("  %s %s\n", mark, a)
		}
		return nil
	},
}

var addonSubscribeCmd = &cobra.Command{
	Use:   "subscribe <project> <service> <addon> [addon ...]",
	Short: "Subscribe a service to project addons (inject their connection secrets)",
	Args:  cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service, add := args[0], args[1], args[2:]
		sub, err := readSubscribedAddons(project, service)
		if err != nil {
			return err
		}
		// Warn (don't fail) on an addon name that isn't in the project — the
		// server is authoritative, but a typo is worth surfacing.
		availSet := map[string]bool{}
		for _, a := range sub.Available {
			availSet[a] = true
		}
		next := append([]string{}, sub.Subscribed...)
		seen := map[string]bool{}
		for _, a := range next {
			seen[a] = true
		}
		for _, a := range add {
			if !availSet[a] {
				fmt.Fprintf(os.Stderr, "warning: %q is not a known addon in project %q (available: %s)\n",
					a, project, strings.Join(sub.Available, ", "))
			}
			if !seen[a] {
				seen[a] = true
				next = append(next, a)
			}
		}
		resp, err := api.SetSubscribedAddons(project, service, next)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("subscribed %s/%s — now subscribed to: %s\n", project, service, strings.Join(next, ", "))
		fmt.Fprintln(os.Stderr, "note: this rolls the service's pod(s) to mount the new connection secret(s).")
		return nil
	},
}

var addonUnsubscribeCmd = &cobra.Command{
	Use:   "unsubscribe <project> <service> <addon> [addon ...]",
	Short: "Unsubscribe a service from project addons (stop injecting their secrets)",
	Args:  cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service, drop := args[0], args[1], args[2:]
		sub, err := readSubscribedAddons(project, service)
		if err != nil {
			return err
		}
		dropSet := map[string]bool{}
		for _, a := range drop {
			dropSet[a] = true
		}
		next := make([]string, 0, len(sub.Subscribed))
		for _, a := range sub.Subscribed {
			if !dropSet[a] {
				next = append(next, a)
			}
		}
		resp, err := api.SetSubscribedAddons(project, service, next)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("unsubscribed %s/%s — now subscribed to: %s\n", project, service,
			strings.Join(next, ", "))
		fmt.Fprintln(os.Stderr, "note: this rolls the service's pod(s).")
		return nil
	},
}

var addonDeleteYes bool

var addonDeleteCmd = &cobra.Command{
	Use:     "delete <project> <name>",
	Aliases: []string{"rm"},
	Short:   "Delete an addon",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(addonDeleteYes,
			fmt.Sprintf("Delete addon %s/%s?", args[0], args[1])); err != nil {
			return err
		}
		resp, err := api.DeleteAddon(args[0], args[1])
		if err != nil {
			return fmt.Errorf("delete addon: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("addon %s/%s deleted\n", args[0], args[1])
		return nil
	},
}

// ---------------- project addon connect-external / resync-external ----------------

var (
	addonExtKind   string
	addonExtSecret string
	addonExtKeys   []string
)

var addonConnectExternalCmd = &cobra.Command{
	Use:   "connect-external <project> <name>",
	Short: "Register an external (managed) datastore as an addon — mirrors an existing kube Secret as <name>-conn",
	Long: `connect-external lets a project use a database that kuso does NOT manage.

Instead of provisioning a StatefulSet, kuso copies the keys from an existing
kube Secret (containing DATABASE_URL / POSTGRES_PASSWORD / etc.) into the
addon's conn secret, so every service in the project can envFrom: it
exactly like a native addon.

Use this for managed Postgres (Hetzner Cloud / Neon / RDS / Supabase) or
managed Redis (Upstash / ElastiCache).`,
	Args: cobra.ExactArgs(2),
	Example: `  kuso project addon connect-external analiz pg --kind postgres --secret hetzner-pg-creds
  kuso project addon connect-external analiz pg --kind postgres --secret hetzner-pg-creds --key DATABASE_URL --key POSTGRES_HOST`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if !contains(supportedAddonKinds, addonExtKind) {
			return fmt.Errorf("--kind must be one of: %s", strings.Join(supportedAddonKinds, ", "))
		}
		req := kusoApi.CreateAddonRequest{
			Name: args[1],
			Kind: addonExtKind,
			External: &kusoApi.AddonExternalRequest{
				SecretName: addonExtSecret,
				SecretKeys: addonExtKeys,
			},
		}
		resp, err := api.AddAddon(args[0], req)
		if err != nil {
			return fmt.Errorf("connect external addon: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("addon %s/%s connected to existing secret %q\n", args[0], args[1], addonExtSecret)
		return nil
	},
}

var addonResyncExternalCmd = &cobra.Command{
	Use:   "resync-external <project> <name>",
	Short: "Re-mirror an external addon's source Secret into its <name>-conn (run after upstream credentials rotated)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ResyncExternalAddon(args[0], args[1])
		if err != nil {
			return fmt.Errorf("resync: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("addon %s/%s conn secret refreshed\n", args[0], args[1])
		return nil
	},
}

// ---------------- project addon connect-instance / resync-instance ----------------

var (
	addonInstName string
)

var addonConnectInstanceCmd = &cobra.Command{
	Use:   "connect-instance <project> <name>",
	Short: "Provision a per-project database on an instance-shared server (admin must register the server via instance secrets first)",
	Long: `connect-instance creates an isolated database for this project on a
shared database server registered cluster-wide.

Setup (admin, once):
  kuso instance-secret set INSTANCE_ADDON_<UPPER>_DSN_ADMIN \
    'postgres://admin:pw@shared-pg.kuso.svc:5432/postgres?sslmode=disable'

Then any project can:
  kuso project addon connect-instance analiz pg --instance pg --kind postgres

The kuso server creates DATABASE "<project>_<addon>" + a matching
role on the shared server, then writes the per-project DSN into
<name>-conn. v0.7.6 supports postgres only.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if addonInstName == "" {
			return fmt.Errorf("--instance is required (the registered shared-addon name)")
		}
		req := kusoApi.CreateAddonRequest{
			Name:             args[1],
			Kind:             addonExtKind,
			UseInstanceAddon: addonInstName,
		}
		if req.Kind == "" {
			req.Kind = "postgres"
		}
		resp, err := api.AddAddon(args[0], req)
		if err != nil {
			return fmt.Errorf("connect instance addon: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("addon %s/%s provisioned on instance addon %q\n", args[0], args[1], addonInstName)
		return nil
	},
}

var addonResyncInstanceCmd = &cobra.Command{
	Use:   "resync-instance <project> <name>",
	Short: "Re-provision the per-project DB on a shared instance addon and rotate the password",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ResyncInstanceAddon(args[0], args[1])
		if err != nil {
			return fmt.Errorf("resync: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("addon %s/%s re-provisioned\n", args[0], args[1])
		return nil
	},
}

var addonRepairPasswordCmd = &cobra.Command{
	Use:   "repair-password <project> <name>",
	Short: "Fix postgres password drift: ALTER USER inside the pod to match the conn secret",
	Long: `Recovers from a known race in the kusoaddon helm chart where
the conn secret's POSTGRES_PASSWORD diverges from the actual password
on disk in the postgres data directory. Result: the SQL console and
any client that reads the conn secret get "password authentication
failed". The repair re-aligns by running ALTER USER inside the pod
via the local trust unix socket.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RepairAddonPassword(args[0], args[1])
		if err != nil {
			return fmt.Errorf("repair: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("addon %s/%s password resynced\n", args[0], args[1])
		return nil
	},
}

// ---------------- env subcommand ----------------

var projectEnvCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage environments within a project",
}

var envDeleteYes bool

var envDeleteCmd = &cobra.Command{
	Use:     "delete <project> <env>",
	Aliases: []string{"rm"},
	Short:   "Delete a preview environment (production cannot be deleted)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(envDeleteYes,
			fmt.Sprintf("Delete preview env %s/%s?", args[0], args[1])); err != nil {
			return err
		}
		resp, err := api.DeleteEnvironment(args[0], args[1])
		if err != nil {
			return fmt.Errorf("delete env: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("env %s/%s deleted\n", args[0], args[1])
		return nil
	},
}

// ---------------- helpers ----------------

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// confirmDestructive prompts the user for a y/N answer on stderr when
// stdin is a TTY AND skip is false. Returns nil to proceed, error to
// abort.
//
// Non-interactive callers (CI, scripts, agents) hit the !isTTY branch
// and proceed without a prompt — the caller has already committed to
// the destructive action by invoking the command. The --yes flag is
// the explicit opt-out for humans who want to skip the prompt.
func confirmDestructive(skip bool, prompt string) error {
	if skip || !stdinIsTTY() {
		return nil
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	var ans string
	if _, err := fmt.Fscanln(os.Stdin, &ans); err != nil {
		return fmt.Errorf("aborted")
	}
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "y", "yes":
		return nil
	default:
		return fmt.Errorf("aborted")
	}
}

// stdinIsTTY reports whether stdin is connected to a real terminal so
// destructive commands don't force a prompt a piped caller can't
// answer.
//
// We use golang.org/x/term.IsTerminal rather than checking
// os.ModeCharDevice — on macOS /dev/null is a character device too, so
// the os.FileMode check would treat `< /dev/null` as a terminal and
// fire the prompt anyway, which is exactly the failure mode we are
// trying to prevent.
func stdinIsTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func init() {
	rootCmd.AddCommand(projectCmd)

	projectCmd.AddCommand(projectCreateCmd)
	projectCreateCmd.Flags().StringVar(&projectCreateRepo, "repo", "", "git repo URL (required)")
	projectCreateCmd.Flags().Int64Var(&projectCreateInstallationID, "github-installation", 0, "GitHub App installation id (use 'kuso github installations' to list)")
	projectCreateCmd.Flags().StringVar(&projectCreateBranch, "branch", "main", "default branch")
	projectCreateCmd.Flags().StringVar(&projectCreateDomain, "domain", "", "base domain (services get <name>.<this>)")
	projectCreateCmd.Flags().BoolVar(&projectCreatePreviews, "previews", false, "enable PR-based preview environments")
	projectCreateCmd.Flags().StringVar(&projectCreateNamespace, "namespace", "", "execution namespace for this project's child resources (default: server's home namespace)")

	projectCmd.AddCommand(projectDeleteCmd)
	projectDeleteCmd.Flags().BoolVarP(&projectDeleteYes, "yes", "y", false, "skip the confirmation prompt")
	projectDeleteCmd.Flags().BoolVar(&projectDeletePurgeData, "purge-data", false, "also delete every addon PVC labeled with this project (default keeps PVCs)")
	projectCmd.AddCommand(projectUpdateCmd)
	projectUpdateCmd.Flags().StringVar(&projectUpdateDescription, "description", "", "new description")
	projectUpdateCmd.Flags().StringVar(&projectUpdateDomain, "domain", "", "new base domain")
	projectUpdateCmd.Flags().StringVar(&projectUpdateRepo, "repo", "", "new default repo URL")
	projectUpdateCmd.Flags().StringVar(&projectUpdateBranch, "branch", "", "new default branch")
	projectUpdateCmd.Flags().Int64Var(&projectUpdateInstallation, "github-installation", 0, "set the bound GitHub App installation id")
	projectUpdateCmd.Flags().BoolVar(&projectUpdateInstallReset, "github-installation-clear", false, "detach the project from any GitHub App installation")
	projectUpdateCmd.Flags().StringVar(&projectUpdatePreviews, "previews", "", "enable/disable preview envs (on|off)")
	projectUpdateCmd.Flags().IntVar(&projectUpdatePreviewsTTL, "previews-ttl", 0, "preview env TTL in days")
	projectUpdateCmd.Flags().StringVar(&projectUpdatePreviewsDomain, "previews-domain", "", "base domain for preview hosts, e.g. tickero.bg (previews become <svc>-pr-N.<domain>); needs wildcard DNS for *.<domain>")
	projectUpdateCmd.Flags().StringVar(&projectUpdateAlwaysOn, "always-on", "", "force every service to never scale to zero (on|off)")
	projectCmd.AddCommand(projectDescribeCmd)
	projectDescribeCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")

	projectCmd.AddCommand(projectServiceCmd)
	projectServiceCmd.AddCommand(serviceAddCmd)
	serviceAddCmd.Flags().StringVar(&serviceAddPath, "path", ".", "monorepo subpath")
	serviceAddCmd.Flags().StringVar(&serviceAddRuntime, "runtime", "nixpacks", "nixpacks|dockerfile|buildpacks|static|image — nixpacks auto-detects most languages with zero config; image deploys an existing registry image without building")
	serviceAddCmd.Flags().StringVar(&serviceAddDockerfile, "dockerfile", "", "Dockerfile filename relative to --path (runtime=dockerfile only; default \"Dockerfile\"), e.g. apps/web/Dockerfile.dev")
	serviceAddCmd.Flags().IntVar(&serviceAddPort, "port", 8080, "container port")
	serviceAddCmd.Flags().StringVar(&serviceAddImageRepo, "image-repo", "", "(runtime=image) registry image, e.g. ghcr.io/owner/app")
	serviceAddCmd.Flags().StringVar(&serviceAddImageTag, "image-tag", "", "(runtime=image) tag — defaults to 'latest' server-side")
	serviceAddCmd.Flags().StringVar(&serviceAddFromService, "from-service", "", "(runtime=worker) sibling service whose image to reuse, e.g. api")
	serviceAddCmd.Flags().StringSliceVar(&serviceAddCommand, "command", nil, "(runtime=worker) container argv, e.g. --command ./worker")
	serviceAddCmd.Flags().IntVar(&serviceAddMinReplicas, "replicas", 0, "minimum replica count (HPA min). Defaults to 1; set 2+ for rolling-update gap-free + node-drain survival.")
	serviceAddCmd.Flags().IntVar(&serviceAddMaxReplicas, "max-replicas", 0, "maximum replica count (HPA max). Defaults to 5, or --replicas value if higher.")
	projectServiceCmd.AddCommand(serviceDeleteCmd)
	serviceDeleteCmd.Flags().BoolVarP(&serviceDeleteYes, "yes", "y", false, "skip the confirmation prompt")
	projectServiceCmd.AddCommand(serviceRenameCmd)
	serviceRenameCmd.Flags().BoolVarP(&serviceRenameYes, "yes", "y", false, "skip the confirmation prompt")
	projectServiceCmd.AddCommand(serviceSetCmd)
	serviceSetCmd.Flags().StringVar(&serviceSetDisplayName, "display-name", "", "free-form label shown on the canvas + overlay header")
	serviceSetCmd.Flags().Int32Var(&serviceSetPort, "port", 8080, "container port")
	serviceSetCmd.Flags().StringVar(&serviceSetRuntime, "runtime", "", "build runtime (dockerfile|nixpacks|buildpacks|static|worker)")
	serviceSetCmd.Flags().StringVar(&serviceSetDomains, "domains", "", "comma- or space-separated custom domains (replaces list; empty clears)")
	serviceSetCmd.Flags().StringVar(&serviceSetInternal, "internal", "", "skip public Ingress (on|off)")
	serviceSetCmd.Flags().StringVar(&serviceSetPrivateEgress, "private-egress", "", "deny public internet egress (on|off)")
	serviceSetCmd.Flags().IntVar(&serviceSetMinReplicas, "replicas", 0, "set minimum replica count (HPA min). 0 keeps current value.")
	serviceSetCmd.Flags().IntVar(&serviceSetMaxReplicas, "max-replicas", 0, "set maximum replica count (HPA max). 0 keeps current value.")
	serviceSetCmd.Flags().StringVar(&serviceSetPath, "path", "", "monorepo subpath relative to repo root (e.g. apps/api)")
	serviceSetCmd.Flags().StringVar(&serviceSetBranch, "branch", "", "git branch override (empty = follow project default)")
	serviceSetCmd.Flags().StringSliceVar(&serviceSetCapAdd, "cap-add", nil, "Linux capability to add back, without CAP_ (repeatable, e.g. --cap-add SETUID --cap-add SETGID)")
	serviceSetCmd.Flags().StringVar(&serviceSetAllowPrivEsc, "allow-privilege-escalation", "", "allow a process to gain more privileges than its parent (on|off)")

	projectCmd.AddCommand(projectAddonCmd)
	projectAddonCmd.AddCommand(addonAddCmd)
	projectAddonCmd.AddCommand(addonListCmd)
	projectAddonCmd.AddCommand(addonSubscribeCmd)
	projectAddonCmd.AddCommand(addonUnsubscribeCmd)
	addonAddCmd.Flags().StringVar(&addonAddKind, "kind", "", "addon kind (required: "+strings.Join(supportedAddonKinds, ", ")+")")
	addonAddCmd.Flags().StringVar(&addonAddVersion, "version", "", "engine version")
	addonAddCmd.Flags().StringVar(&addonAddSize, "size", "small", "small|medium|large")
	addonAddCmd.Flags().BoolVar(&addonAddHA, "ha", false, "use clustered chart variant where supported")
	_ = addonAddCmd.MarkFlagRequired("kind")
	projectAddonCmd.AddCommand(addonDeleteCmd)
	addonDeleteCmd.Flags().BoolVarP(&addonDeleteYes, "yes", "y", false, "skip the confirmation prompt")

	projectAddonCmd.AddCommand(addonConnectExternalCmd)
	addonConnectExternalCmd.Flags().StringVar(&addonExtKind, "kind", "", "addon kind (required: postgres, redis, ...)")
	addonConnectExternalCmd.Flags().StringVar(&addonExtSecret, "secret", "", "name of an existing kube Secret to mirror as the addon's conn secret (required)")
	addonConnectExternalCmd.Flags().StringSliceVar(&addonExtKeys, "key", nil, "optional key allowlist; repeat or comma-separate. Empty = mirror every key")
	_ = addonConnectExternalCmd.MarkFlagRequired("kind")
	_ = addonConnectExternalCmd.MarkFlagRequired("secret")

	projectAddonCmd.AddCommand(addonResyncExternalCmd)

	projectAddonCmd.AddCommand(addonConnectInstanceCmd)
	addonConnectInstanceCmd.Flags().StringVar(&addonInstName, "instance", "", "name of the registered shared addon (required, matches INSTANCE_ADDON_<UPPER>_DSN_ADMIN)")
	addonConnectInstanceCmd.Flags().StringVar(&addonExtKind, "kind", "postgres", "addon kind (only postgres is supported in v0.7.6)")
	_ = addonConnectInstanceCmd.MarkFlagRequired("instance")

	projectAddonCmd.AddCommand(addonResyncInstanceCmd)
	projectAddonCmd.AddCommand(addonRepairPasswordCmd)

	projectCmd.AddCommand(projectEnvCmd)
	projectEnvCmd.AddCommand(envDeleteCmd)
	envDeleteCmd.Flags().BoolVarP(&envDeleteYes, "yes", "y", false, "skip the confirmation prompt")

	// Top-level alias: `kuso service add <project> <name>` → same as
	// `kuso project service add`. Flag vars (serviceAddPath, ...) are
	// shared globals so the same flags work on both forms.
	rootCmd.AddCommand(serviceCmd)
	serviceCmd.AddCommand(serviceAddTopCmd)
	serviceAddTopCmd.Flags().StringVar(&serviceAddPath, "path", ".", "monorepo subpath")
	serviceAddTopCmd.Flags().StringVar(&serviceAddRuntime, "runtime", "nixpacks", "nixpacks|dockerfile|buildpacks|static|image — nixpacks auto-detects most languages with zero config; image deploys an existing registry image without building")
	serviceAddTopCmd.Flags().StringVar(&serviceAddDockerfile, "dockerfile", "", "Dockerfile filename relative to --path (runtime=dockerfile only; default \"Dockerfile\"), e.g. apps/web/Dockerfile.dev")
	serviceAddTopCmd.Flags().IntVar(&serviceAddPort, "port", 8080, "container port")
	serviceAddTopCmd.Flags().StringVar(&serviceAddImageRepo, "image-repo", "", "(runtime=image) registry image, e.g. ghcr.io/owner/app")
	serviceAddTopCmd.Flags().StringVar(&serviceAddImageTag, "image-tag", "", "(runtime=image) tag — defaults to 'latest' server-side")
	serviceAddTopCmd.Flags().StringVar(&serviceAddFromService, "from-service", "", "(runtime=worker) sibling service whose image to reuse, e.g. api")
	serviceAddTopCmd.Flags().StringSliceVar(&serviceAddCommand, "command", nil, "(runtime=worker) container argv, e.g. --command ./worker")
	serviceAddTopCmd.Flags().IntVar(&serviceAddMinReplicas, "replicas", 0, "minimum replica count (HPA min). Defaults to 1; set 2+ for rolling-update gap-free + node-drain survival.")
	serviceAddTopCmd.Flags().IntVar(&serviceAddMaxReplicas, "max-replicas", 0, "maximum replica count (HPA max). Defaults to 5, or --replicas value if higher.")
	// Top-level alias `kuso service set` mirrors the long form. Cobra
	// commands can't have two parents, so we mint a fresh shell and
	// dispatch to the same RunE + share the flag vars (already
	// package-level globals).
	serviceCmd.AddCommand(serviceSetTopCmd)
	serviceSetTopCmd.Flags().StringVar(&serviceSetDisplayName, "display-name", "", "free-form label shown on the canvas + overlay header")
	serviceSetTopCmd.Flags().Int32Var(&serviceSetPort, "port", 8080, "container port")
	serviceSetTopCmd.Flags().StringVar(&serviceSetRuntime, "runtime", "", "build runtime (dockerfile|nixpacks|buildpacks|static|worker)")
	serviceSetTopCmd.Flags().StringVar(&serviceSetDomains, "domains", "", "comma- or space-separated custom domains (replaces list; empty clears)")
	serviceSetTopCmd.Flags().StringVar(&serviceSetInternal, "internal", "", "skip public Ingress (on|off)")
	serviceSetTopCmd.Flags().StringVar(&serviceSetPrivateEgress, "private-egress", "", "deny public internet egress (on|off)")
	serviceSetTopCmd.Flags().IntVar(&serviceSetMinReplicas, "replicas", 0, "set minimum replica count (HPA min). 0 keeps current value.")
	serviceSetTopCmd.Flags().IntVar(&serviceSetMaxReplicas, "max-replicas", 0, "set maximum replica count (HPA max). 0 keeps current value.")
	serviceSetTopCmd.Flags().StringVar(&serviceSetPath, "path", "", "monorepo subpath relative to repo root (e.g. apps/api)")
	serviceSetTopCmd.Flags().StringVar(&serviceSetBranch, "branch", "", "git branch override (empty = follow project default)")
}

// serviceSetTopCmd is the top-level `kuso service set` shell. Same
// RunE as serviceSetCmd because the long form (`kuso project service
// set`) and the short form should behave identically — defining the
// dispatcher once in a shared closure keeps that promise enforced
// instead of relying on us remembering to keep two RunE's in sync.
var serviceSetTopCmd = &cobra.Command{
	Use:   "set <project> <service>",
	Short: "Edit a service's spec (alias for `kuso project service set`)",
	Args:  cobra.ExactArgs(2),
	RunE:  func(cmd *cobra.Command, args []string) error { return serviceSetCmd.RunE(cmd, args) },
}
