// Package kusoCli is the cobra-rooted CLI binary that ships as `kuso`.
// Each command lives in its own file (login.go, project.go, …) and
// registers itself onto rootCmd via init(); Execute() wires up the
// shared resty client, loads ~/.kuso config, and hands off to cobra.
package kusoCli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"kuso/cmd/kusoCli/version"
	"kuso/pkg/kusoApi"
)

// Package-level state shared across commands. The CLI is single-shot
// (run, exit) so this state is initialised once at Execute() and never
// mutated concurrently.
var (
	api *kusoApi.KusoClient

	// instanceList + currentInstanceName are populated from
	// ~/.kuso/kuso.yaml. instanceNameList is the ordered slice for
	// table rendering (`kuso remote`) and survey prompts.
	instanceList        map[string]Instance
	instanceNameList    []string
	currentInstanceName string
	currentInstance     Instance

	// outputFormat is wired by `kuso get -o json` and read by table
	// renderers to decide between human + machine output.
	outputFormat string

	// force suppresses interactive prompts in scripted contexts. Set
	// by per-command flags; respected by promptLine.
	force bool
)

var rootCmd = &cobra.Command{
	Use:   "kuso",
	Short: "kuso — a self-hosted Kubernetes-native PaaS",
	// A failure inside a command's RunE is a RUNTIME error (the run failed, the
	// server 500'd, the build errored) — not the user misusing the CLI. Cobra's
	// default dumps the full usage/help block on any RunE error, which buries the
	// real message under a wall of flags (e.g. `kuso run … -f` printing Usage
	// after a seed's FK violation). Silence it: arg/flag PARSE errors still show
	// usage (cobra handles those before RunE). SilenceErrors lets Execute() print
	// the error exactly once instead of cobra + Execute() printing it twice.
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `kuso ships your code from a git repo to a running URL on a
Kubernetes cluster you control. Project graph, services, environments,
addons, builds, secrets — all driven by a small set of CRDs reconciled
by a helm-operator.

Run ` + "`kuso login`" + ` once to point at a kuso server, then explore the
command tree.`,
	Example: `  kuso login --api https://kuso.example.com
  kuso project create my-app --repo https://github.com/me/my-app
  kuso get projects -o json
  kuso logs my-app web --follow`,
}

// Execute is the entry point called by cmd/main.go. Wires up shared
// state, registers commands (which is done via init() in their own
// files), and hands off to cobra.
func Execute() {
	rootCmd.CompletionOptions.HiddenDefaultCmd = false
	rootCmd.AddCommand(version.CliCommand())
	setUsageTemplate(rootCmd)

	loadInstances()
	loadCredentials()

	// Initialise the API client up front so commands can call methods
	// on it even when the user isn't logged in yet (login itself still
	// works, since it uses Login() with the URL but no token).
	api = &kusoApi.KusoClient{}
	tok := ""
	if currentInstanceName != "" {
		tok = credentialsConfig.GetString(currentInstanceName)
	}
	api.Init(currentInstance.ApiUrl, tok)

	for _, cmd := range rootCmd.Commands() {
		setUsageTemplate(cmd)
	}

	if err := rootCmd.Execute(); err != nil {
		// With SilenceErrors set on rootCmd, cobra no longer prints the error
		// itself — so this is the single place the message is shown, then exit 1.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
