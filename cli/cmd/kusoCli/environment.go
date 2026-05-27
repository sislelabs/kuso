// Environment lifecycle: create/delete a KusoEnvironment for a
// service. Distinct from `kuso env` which manages env VARIABLES
// (KusoService.spec.envVars). The naming gap is real but the wire
// resources are different — env vars are settings on a service;
// environments are independent deploy targets that mirror it.
//
// Usage:
//   kuso environment add <project> <service> <env-name> --branch <branch> [--host <host>]
//   kuso environment delete <project> <env-name>
//   kuso environment list <project>           # alias for `kuso get envs`
//
// Top-level command is `environment` (no `env` alias because `env`
// already means env-vars). Also mirrored as `kuso project env add` for
// consistency with `kuso project service add`.

package kusoCli

import (
	"encoding/json"
	"fmt"

	"kuso/pkg/kusoApi"

	"github.com/spf13/cobra"
)

var (
	environmentAddBranch string
	environmentAddHost   string
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

This is distinct from "kuso env" which manages environment VARIABLES on
a service.`,
}

var environmentAddCmd = &cobra.Command{
	Use:   "add <project> <service> <name>",
	Short: "Create a new environment for a service (e.g. staging)",
	Args:  cobra.ExactArgs(3),
	Example: `  kuso environment add tickero api staging --branch staging
  kuso environment add tickero frontend qa --branch develop --host qa.tickero.bg`,
	RunE: runEnvironmentAdd,
}

// Mirrored under `kuso project env add` for parallelism with
// `kuso project service add`. Cobra requires a distinct *cobra.Command
// per parent, so this is a thin shell sharing the RunE closure.
var projectEnvAddCmd = &cobra.Command{
	Use:   "add <project> <service> <name>",
	Short: "Create a new environment for a service (alias for `kuso environment add`)",
	Args:  cobra.ExactArgs(3),
	RunE:  runEnvironmentAdd,
}

func init() {
	rootCmd.AddCommand(environmentCmd)
	environmentCmd.AddCommand(environmentAddCmd)
	environmentAddCmd.Flags().StringVar(&environmentAddBranch, "branch", "", "git branch this env builds from (required)")
	environmentAddCmd.Flags().StringVar(&environmentAddHost, "host", "", "override the auto-generated host (default: <env>.<service>.<baseDomain>)")
	_ = environmentAddCmd.MarkFlagRequired("branch")

	// Project-scoped alias. projectEnvCmd is already registered in
	// project.go; we just hang `add` off of it. Delete is already
	// registered there too.
	projectEnvCmd.AddCommand(projectEnvAddCmd)
	projectEnvAddCmd.Flags().StringVar(&environmentAddBranch, "branch", "", "git branch this env builds from (required)")
	projectEnvAddCmd.Flags().StringVar(&environmentAddHost, "host", "", "override the auto-generated host")
	_ = projectEnvAddCmd.MarkFlagRequired("branch")
}
