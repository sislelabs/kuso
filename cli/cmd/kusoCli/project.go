package kusoCli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
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
	projectCreateRepo            string
	projectCreateBranch          string
	projectCreateDomain          string
	projectCreatePreviews        bool
	projectCreateInstallationID  int64
	projectCreateNamespace       string
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
		}
		req.DefaultRepo.URL = projectCreateRepo
		req.DefaultRepo.DefaultBranch = projectCreateBranch
		req.Previews.Enabled = projectCreatePreviews
		if projectCreateInstallationID > 0 {
			req.GitHub = &struct {
				InstallationID int64 `json:"installationId,omitempty"`
			}{InstallationID: projectCreateInstallationID}
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
	projectUpdateBranch        string
	projectUpdateRepo          string
	projectUpdateDomain        string
	projectUpdateDescription   string
	projectUpdateInstallation  int64
	projectUpdateInstallReset  bool
	projectUpdatePreviews      string // "on" | "off" | "" (leave alone)
	projectUpdatePreviewsTTL   int
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
			req.DefaultRepo = &struct {
				URL           string `json:"url,omitempty"`
				DefaultBranch string `json:"defaultBranch,omitempty"`
			}{URL: projectUpdateRepo, DefaultBranch: projectUpdateBranch}
		}
		if projectUpdateInstallReset {
			req.GitHub = &struct {
				InstallationID int64 `json:"installationId,omitempty"`
			}{InstallationID: 0}
		} else if cmd.Flags().Changed("github-installation") {
			req.GitHub = &struct {
				InstallationID int64 `json:"installationId,omitempty"`
			}{InstallationID: projectUpdateInstallation}
		}
		if cmd.Flags().Changed("previews") || cmd.Flags().Changed("previews-ttl") {
			pv := &struct {
				Enabled *bool `json:"enabled,omitempty"`
				TTLDays *int  `json:"ttlDays,omitempty"`
			}{}
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
			req.Previews = pv
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

var projectDeleteYes bool

var projectDeleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a project (cascades to services, envs, addons)",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(projectDeleteYes,
			fmt.Sprintf("Delete project %q (cascades to services, envs, addons)?", args[0])); err != nil {
			return err
		}
		resp, err := api.DeleteProject(args[0])
		if err != nil {
			return fmt.Errorf("delete: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("project %s deleted\n", args[0])
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
	serviceAddPath    string
	serviceAddRuntime string
	serviceAddPort    int
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
	req := kusoApi.CreateServiceRequest{Name: args[1]}
	req.Repo.Path = serviceAddPath
	req.Runtime = serviceAddRuntime
	req.Port = serviceAddPort
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
	Example: `  kuso project service add analiz api --runtime dockerfile --port 8080
  kuso project service add analiz web --path apps/web --runtime nixpacks --port 3000
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

// ---------------- project addon add ----------------

var (
	addonAddKind    string
	addonAddVersion string
	addonAddSize    string
	addonAddHA      bool
)

var supportedAddonKinds = []string{
	"postgres", "redis", "mongodb", "mysql", "rabbitmq", "memcached",
	"clickhouse", "elasticsearch", "kafka", "cockroachdb", "couchdb",
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
	projectCmd.AddCommand(projectUpdateCmd)
	projectUpdateCmd.Flags().StringVar(&projectUpdateDescription, "description", "", "new description")
	projectUpdateCmd.Flags().StringVar(&projectUpdateDomain, "domain", "", "new base domain")
	projectUpdateCmd.Flags().StringVar(&projectUpdateRepo, "repo", "", "new default repo URL")
	projectUpdateCmd.Flags().StringVar(&projectUpdateBranch, "branch", "", "new default branch")
	projectUpdateCmd.Flags().Int64Var(&projectUpdateInstallation, "github-installation", 0, "set the bound GitHub App installation id")
	projectUpdateCmd.Flags().BoolVar(&projectUpdateInstallReset, "github-installation-clear", false, "detach the project from any GitHub App installation")
	projectUpdateCmd.Flags().StringVar(&projectUpdatePreviews, "previews", "", "enable/disable preview envs (on|off)")
	projectUpdateCmd.Flags().IntVar(&projectUpdatePreviewsTTL, "previews-ttl", 0, "preview env TTL in days")
	projectCmd.AddCommand(projectDescribeCmd)
	projectDescribeCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")

	projectCmd.AddCommand(projectServiceCmd)
	projectServiceCmd.AddCommand(serviceAddCmd)
	serviceAddCmd.Flags().StringVar(&serviceAddPath, "path", ".", "monorepo subpath")
	serviceAddCmd.Flags().StringVar(&serviceAddRuntime, "runtime", "dockerfile", "dockerfile|nixpacks|buildpacks|static")
	serviceAddCmd.Flags().IntVar(&serviceAddPort, "port", 8080, "container port")
	projectServiceCmd.AddCommand(serviceDeleteCmd)
	serviceDeleteCmd.Flags().BoolVarP(&serviceDeleteYes, "yes", "y", false, "skip the confirmation prompt")
	projectServiceCmd.AddCommand(serviceRenameCmd)
	serviceRenameCmd.Flags().BoolVarP(&serviceRenameYes, "yes", "y", false, "skip the confirmation prompt")

	projectCmd.AddCommand(projectAddonCmd)
	projectAddonCmd.AddCommand(addonAddCmd)
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
	serviceAddTopCmd.Flags().StringVar(&serviceAddRuntime, "runtime", "dockerfile", "dockerfile|nixpacks|buildpacks|static")
	serviceAddTopCmd.Flags().IntVar(&serviceAddPort, "port", 8080, "container port")
}
