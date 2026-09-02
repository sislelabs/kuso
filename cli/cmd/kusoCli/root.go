// Package kusoCli is the cobra-rooted CLI binary that ships as `kuso`.
// Each command lives in its own file (login.go, project.go, …) and
// registers itself onto rootCmd via init(); Execute() wires up the
// shared resty client, loads ~/.kuso config, and hands off to cobra.
package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
	//
	// IMPORTANT: every command binding this variable MUST register the
	// same default ("table"). pflag writes a flag's default into the
	// bound variable at registration time, so with one shared variable
	// the LAST init() to run decides the value for EVERY command —
	// init() order is alphabetical by filename, which is not something
	// a command author can reason about locally. Four JSON-only
	// commands used to register "json" here and, via user.go sorting
	// last, silently flipped the default for all ~48 table commands to
	// json. Those now bind outputFormatJSONOnly below.
	outputFormat string

	// outputFormatJSONOnly backs the handful of commands that emit JSON
	// unconditionally (their -o flag is accepted for symmetry but the
	// renderer ignores it). Kept separate so their "json" default can
	// never leak into the shared outputFormat above.
	outputFormatJSONOnly string

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
	Example: `  kuso login --api https://kuso.example.com          # once, per instance
  kuso doctor                                       # verify session + DNS + webhooks
  kuso project create my-app --repo https://github.com/me/my-app
  kuso status my-app                                # services, URLs, replicas, builds
  kuso logs my-app web --follow                     # tail production logs
  kuso db sql my-app my-app-db "SELECT count(*) FROM users"

Deploying is a git push: kuso builds and rolls the new pod for you.
Use 'kuso build trigger' only for an out-of-band rebuild.
`,
}

// Execute is the entry point called by cmd/main.go. Wires up shared
// state, registers commands (which is done via init() in their own
// files), and hands off to cobra.
func Execute() {
	rootCmd.CompletionOptions.HiddenDefaultCmd = false
	rootCmd.AddCommand(version.CliCommand())
	registerCommandGroups(rootCmd)
	registerCompletions(rootCmd)
	setUsageTemplate(rootCmd)

	loadInstances()
	loadCredentials()

	// Initialise the API client up front so commands can call methods
	// on it even when the user isn't logged in yet (login itself still
	// works, since it uses Login() with the URL but no token).
	api = &kusoApi.KusoClient{}
	// KUSO_TOKEN beats saved credentials — same precedence `kuso doctor`
	// (doctor.go) and the MCP server (mcp/internal/config) already use.
	//
	// This path used to read the credentials file ONLY, so a container
	// or CI job that exported KUSO_TOKEN got the worst possible
	// outcome: `kuso doctor` reported "[PASS] token" (it does check the
	// env var) while every real command failed "not logged in; run
	// 'kuso login' first". The diagnostic tool actively disagreed with
	// the thing it was diagnosing.
	tok := strings.TrimSpace(os.Getenv("KUSO_TOKEN"))
	if tok == "" && currentInstanceName != "" {
		tok = credentialsConfig.GetString(currentInstanceName)
	}
	// KUSO_API_URL likewise lets a headless caller point at an instance
	// without a prior `kuso remote add`; the saved instance still wins
	// when the env var is unset.
	apiURL := currentInstance.ApiUrl
	if v := strings.TrimSpace(os.Getenv("KUSO_API_URL")); v != "" {
		apiURL = v
	}
	api.Init(apiURL, tok)
	version.ServerVersion = func() (string, error) {
		if tok == "" {
			return "", fmt.Errorf("not logged in; run 'kuso login'")
		}
		resp, err := api.RawGet("/api/system/version")
		if err := checkRespErr(resp, err); err != nil {
			return "", err
		}
		var v struct {
			Current string `json:"current"`
		}
		if err := json.Unmarshal(resp.Body(), &v); err != nil {
			return "", fmt.Errorf("decode version: %w", err)
		}
		return v.Current + " (" + apiURL + ")", nil
	}

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
