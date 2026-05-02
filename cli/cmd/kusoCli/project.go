package kusoCli

import (
	"encoding/json"
	"fmt"
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
		req := kusoApi.CreateProjectRequest{Name: args[0], BaseDomain: projectCreateDomain}
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

var serviceAddCmd = &cobra.Command{
	Use:   "add <project> <name>",
	Short: "Add a service to a project (creates production env automatically)",
	Args:  cobra.ExactArgs(2),
	Example: `  kuso project service add analiz api --runtime dockerfile --port 8080
  kuso project service add analiz web --path apps/web --runtime nixpacks --port 3000`,
	RunE: func(cmd *cobra.Command, args []string) error {
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
	},
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

	projectCmd.AddCommand(projectDeleteCmd)
	projectDeleteCmd.Flags().BoolVarP(&projectDeleteYes, "yes", "y", false, "skip the confirmation prompt")
	projectCmd.AddCommand(projectDescribeCmd)
	projectDescribeCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")

	projectCmd.AddCommand(projectServiceCmd)
	projectServiceCmd.AddCommand(serviceAddCmd)
	serviceAddCmd.Flags().StringVar(&serviceAddPath, "path", ".", "monorepo subpath")
	serviceAddCmd.Flags().StringVar(&serviceAddRuntime, "runtime", "dockerfile", "dockerfile|nixpacks|buildpacks|static")
	serviceAddCmd.Flags().IntVar(&serviceAddPort, "port", 8080, "container port")
	projectServiceCmd.AddCommand(serviceDeleteCmd)
	serviceDeleteCmd.Flags().BoolVarP(&serviceDeleteYes, "yes", "y", false, "skip the confirmation prompt")

	projectCmd.AddCommand(projectAddonCmd)
	projectAddonCmd.AddCommand(addonAddCmd)
	addonAddCmd.Flags().StringVar(&addonAddKind, "kind", "", "addon kind (required: "+strings.Join(supportedAddonKinds, ", ")+")")
	addonAddCmd.Flags().StringVar(&addonAddVersion, "version", "", "engine version")
	addonAddCmd.Flags().StringVar(&addonAddSize, "size", "small", "small|medium|large")
	addonAddCmd.Flags().BoolVar(&addonAddHA, "ha", false, "use clustered chart variant where supported")
	_ = addonAddCmd.MarkFlagRequired("kind")
	projectAddonCmd.AddCommand(addonDeleteCmd)
	addonDeleteCmd.Flags().BoolVarP(&addonDeleteYes, "yes", "y", false, "skip the confirmation prompt")

	projectCmd.AddCommand(projectEnvCmd)
	projectEnvCmd.AddCommand(envDeleteCmd)
	envDeleteCmd.Flags().BoolVarP(&envDeleteYes, "yes", "y", false, "skip the confirmation prompt")
}
