// Environment lifecycle: create/delete a KusoEnvironment for a
// service. Distinct from `kuso env` which manages env VARIABLES
// (KusoService.spec.envVars). The naming gap is real but the wire
// resources are different — env vars are settings on a service;
// environments are independent deploy targets that mirror it.
//
// Usage:
//   kuso environment add <project> <service> <env-name> --branch <branch> [--host <host>]
//   kuso environment delete <project> <env-name>     # alias: rm
//   kuso environment list <project>                  # same data as `kuso get envs`
//
// Top-level command is `environment` (no `env` alias because `env`
// already means env-vars). Also mirrored as `kuso project env add` for
// consistency with `kuso project service add`.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"kuso/pkg/kusoApi"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var (
	environmentAddBranch      string
	environmentAddHost        string
	environmentAddShareAddons bool
	environmentAddSeedFrom    string
	environmentAddAddons      []string
)

// runEnvironmentAdd is shared by both the top-level `kuso environment
// add` and the project-scoped `kuso project env add` aliases.
var runEnvironmentAdd = func(cmd *cobra.Command, args []string) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	if environmentAddBranch == "" {
		return fmt.Errorf("--branch is required (which branch should this env build from?)")
	}
	req := kusoApi.CreateEnvRequest{
		Name:         args[2],
		Branch:       environmentAddBranch,
		HostOverride: environmentAddHost,
		ShareAddons:  environmentAddShareAddons,
		SeedFrom:     environmentAddSeedFrom,
		Addons:       environmentAddAddons,
	}
	resp, err := api.AddEnvironment(args[0], args[1], req)
	if err != nil {
		return fmt.Errorf("add environment: %w", err)
	}
	if resp.StatusCode() >= 300 {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
	}
	var data map[string]any
	_ = json.Unmarshal(resp.Body(), &data)
	host := ""
	if spec, ok := data["spec"].(map[string]any); ok {
		if h, ok := spec["host"].(string); ok {
			host = h
		}
	}
	if host == "" {
		fmt.Printf("environment %s-%s-%s added (branch=%s)\n", args[0], args[1], args[2], environmentAddBranch)
	} else {
		fmt.Printf("environment %s-%s-%s added (branch=%s, host=%s)\n", args[0], args[1], args[2], environmentAddBranch, host)
	}
	fmt.Printf("trigger the first build with: kuso build trigger %s %s --branch %s\n", args[0], args[1], environmentAddBranch)
	return nil
}

// Top-level command: `kuso environment ...`. The plain `env` alias is
// deliberately not added — it collides with the env-vars command.

var environmentCmd = &cobra.Command{
	Use:     "environment",
	Aliases: []string{"environments"},
	Short:   "Manage long-lived environments (staging, qa, ...) on a service",
	Long: `Manage long-lived KusoEnvironment resources on a service.

Each KusoService gets a "production" env created automatically. Use this
command to add a "staging", "qa", or per-branch env that builds from a
different git branch and (optionally) serves on a different host.

By default a new env gets its OWN addons (its own postgres DB, redis, s3) so
staging/qa never touch production data — the same isolation PR previews get.
The postgres DB starts empty; pass --seed-from <env> to copy a snapshot, or
--share-addons to fall back to sharing the project's production addons.

This is distinct from "kuso env" which manages environment VARIABLES on
a service.`,
}

var environmentAddCmd = &cobra.Command{
	Use:   "add <project> <service> <name>",
	Short: "Create a new environment for a service (e.g. staging)",
	Args:  cobra.ExactArgs(3),
	Example: `  kuso environment add tickero api staging --branch staging
  kuso environment add tickero api staging --branch staging --seed-from production
  kuso environment add tickero api staging --branch staging --share-addons
  kuso environment add tickero frontend qa --branch develop --host qa.tickero.bg`,
	RunE: runEnvironmentAdd,
}

var environmentDeleteYes bool

// environmentDeleteCmd is the discoverable top-level mirror of
// `kuso project env delete`. Both share the DeleteEnvironment API. Env
// names are the full CR name (e.g. tickero-api-staging) — the same form
// `kuso environment list` / `kuso get envs` print.
var environmentDeleteCmd = &cobra.Command{
	Use:     "delete <project> <env>",
	Aliases: []string{"rm"},
	Short:   "Delete an environment (production cannot be deleted)",
	Args:    cobra.ExactArgs(2),
	Example: `  kuso environment delete tickero tickero-api-staging
  kuso environment rm tickero tickero-api-staging --yes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(environmentDeleteYes,
			fmt.Sprintf("Delete environment %s/%s?", args[0], args[1])); err != nil {
			return err
		}
		resp, err := api.DeleteEnvironment(args[0], args[1])
		if err != nil {
			return fmt.Errorf("delete env: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("environment %s/%s deleted\n", args[0], args[1])
		return nil
	},
}

// environmentListCmd is the discoverable mirror of `kuso get envs`. Same
// data, same -o json support, rendered here so a user who found
// `kuso environment add` doesn't have to know about the `get` tree.
var environmentListCmd = &cobra.Command{
	Use:     "list <project>",
	Aliases: []string{"ls"},
	Short:   "List environments in a project",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetEnvironments(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("fetch environments: %w", err)
		}
		var items []map[string]any
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		sort.Slice(items, func(i, j int) bool {
			return resourceName(items[i]) < resourceName(items[j])
		})
		switch outputFormat {
		case "json":
			return jsonOut(items)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "SERVICE", "KIND", "BRANCH", "HOST"})
			for _, e := range items {
				spec := mapAt(e, "spec")
				t.Append([]string{
					resourceName(e),
					stripPrefix(asString(spec["service"]), args[0]+"-"),
					asString(spec["kind"]),
					asString(spec["branch"]),
					asString(spec["host"]),
				})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

// Mirrored under `kuso project env add` for parallelism with
// `kuso project service add`. Cobra requires a distinct *cobra.Command
// per parent, so this is a thin shell sharing the RunE closure.
// (`delete`/`list` are registered directly on environmentCmd in init();
// `kuso project env delete` remains as the project-scoped mirror.)
var projectEnvAddCmd = &cobra.Command{
	Use:   "add <project> <service> <name>",
	Short: "Create a new environment for a service (alias for `kuso environment add`)",
	Args:  cobra.ExactArgs(3),
	RunE:  runEnvironmentAdd,
}

func init() {
	rootCmd.AddCommand(environmentCmd)
	environmentCmd.AddCommand(environmentAddCmd)
	environmentCmd.AddCommand(environmentDeleteCmd)
	environmentCmd.AddCommand(environmentListCmd)
	environmentDeleteCmd.Flags().BoolVarP(&environmentDeleteYes, "yes", "y", false, "skip the confirmation prompt")
	environmentListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")
	environmentAddCmd.Flags().StringVar(&environmentAddBranch, "branch", "", "git branch this env builds from (required)")
	environmentAddCmd.Flags().StringVar(&environmentAddHost, "host", "", "override the auto-generated host (default: <env>.<service>.<baseDomain>)")
	environmentAddCmd.Flags().BoolVar(&environmentAddShareAddons, "share-addons", false, "share the project's addons with production instead of giving this env its own DB/redis/s3 (legacy behavior)")
	environmentAddCmd.Flags().StringVar(&environmentAddSeedFrom, "seed-from", "", "seed this env's postgres DB from the named source env (default: empty DB)")
	environmentAddCmd.Flags().StringSliceVar(&environmentAddAddons, "addons", nil, "stateful addon kinds to provision per-env (default: all the project has — postgres,redis,s3)")
	_ = environmentAddCmd.MarkFlagRequired("branch")

	// Project-scoped alias. projectEnvCmd is already registered in
	// project.go; we just hang `add` off of it. Delete is already
	// registered there too.
	projectEnvCmd.AddCommand(projectEnvAddCmd)
	projectEnvAddCmd.Flags().StringVar(&environmentAddBranch, "branch", "", "git branch this env builds from (required)")
	projectEnvAddCmd.Flags().StringVar(&environmentAddHost, "host", "", "override the auto-generated host")
	projectEnvAddCmd.Flags().BoolVar(&environmentAddShareAddons, "share-addons", false, "share the project's addons instead of giving this env its own DB/redis/s3")
	projectEnvAddCmd.Flags().StringVar(&environmentAddSeedFrom, "seed-from", "", "seed this env's postgres DB from the named source env (default: empty DB)")
	projectEnvAddCmd.Flags().StringSliceVar(&environmentAddAddons, "addons", nil, "stateful addon kinds to provision per-env (default: all the project has)")
	_ = projectEnvAddCmd.MarkFlagRequired("branch")
}
