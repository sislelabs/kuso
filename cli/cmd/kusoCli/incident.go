package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso incident` — read + close the autonomous incident-response
// agent's incidents.
//
//   kuso incident list
//   kuso incident show <id>
//   kuso incident resolve <id>
//
// `kuso incident-agent set-credentials` is a separate top-level command
// (it doesn't talk to the kuso API — it prints the kubectl the operator
// runs to seed the agent's Claude Code OAuth creds into the cluster).

var incidentCmd = &cobra.Command{
	Use:   "incident",
	Short: "Inspect and resolve incident-agent incidents",
}

var incidentListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List incidents (newest first)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListIncidents()
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		// Server wraps the list as {"incidents": [...]} (matches the web
		// client + the other list endpoints' envelope shape).
		var envelope struct {
			Incidents []kusoApi.Incident `json:"incidents"`
		}
		if err := json.Unmarshal(resp.Body(), &envelope); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		incidents := envelope.Incidents
		switch outputFormat {
		case "json":
			return jsonOut(incidents)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "TYPE", "TARGET", "STATE", "AGE", "PR"})
			for _, in := range incidents {
				t.Append([]string{
					in.ID,
					in.EventType,
					incidentTarget(in),
					in.State,
					incidentAge(in.CreatedAt),
					incidentPRCell(in),
				})
			}
			t.Render()
			return nil
		}
	},
}

var incidentShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show an incident's findings, feedback, and PR",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetIncident(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("incident %s not found", args[0])
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var in kusoApi.Incident
		if err := json.Unmarshal(resp.Body(), &in); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if outputFormat == "json" {
			return jsonOut(in)
		}

		fmt.Printf("Incident   %s\n", in.ID)
		fmt.Printf("Type       %s\n", in.EventType)
		fmt.Printf("Target     %s\n", incidentTarget(in))
		fmt.Printf("State      %s\n", in.State)
		if in.Severity != "" {
			fmt.Printf("Severity   %s\n", in.Severity)
		}
		if in.Title != "" {
			fmt.Printf("Title      %s\n", in.Title)
		}
		fmt.Printf("Created    %s (%s ago)\n", in.CreatedAt.Format(time.RFC3339), incidentAge(in.CreatedAt))
		if in.PRUrl != "" {
			fmt.Printf("PR         #%d %s\n", in.PRNumber, in.PRUrl)
		}
		if in.DiscordThread != "" {
			fmt.Printf("Thread     %s\n", in.DiscordThread)
		}

		fmt.Println()
		fmt.Println("── Findings ──────────────────────────────")
		if strings.TrimSpace(in.Findings) == "" {
			fmt.Println("(none yet — agent is still investigating)")
		} else {
			fmt.Println(strings.TrimRight(in.Findings, "\n"))
		}

		if len(in.Feedback) > 0 {
			fmt.Println()
			fmt.Println("── Feedback ──────────────────────────────")
			for _, fb := range in.Feedback {
				ts := fb.At.Format("2006-01-02 15:04")
				switch {
				case fb.Decision != "":
					fmt.Printf("[%s] decision: %s\n", ts, fb.Decision)
				default:
					fmt.Printf("[%s] %s\n", ts, fb.Text)
				}
			}
		}
		return nil
	},
}

var incidentResolveCmd = &cobra.Command{
	Use:   "resolve <id>",
	Short: "Resolve (close) an incident",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ResolveIncident(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("incident %s not found", args[0])
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("incident %s resolved\n", args[0])
		return nil
	},
}

// `kuso incident-agent set-credentials` — print the kubectl the operator
// runs to seed the incident agent's Claude Code OAuth creds into the
// cluster. Deliberately a print-the-command helper (not an API upload)
// so the operator's personal CC session token never traverses the kuso
// API. The operator pipes the printed command into their shell.
var incidentAgentCmd = &cobra.Command{
	Use:   "incident-agent",
	Short: "Helpers for the incident-response agent",
}

var (
	incidentAgentSetCredsNamespace string
	incidentAgentCredsFile         string
	incidentAgentPrintKubectl      bool
)

var incidentAgentSetCredsCmd = &cobra.Command{
	Use:   "set-credentials",
	Short: "Upload your Claude Code credentials to the incident agent",
	Long: `Reads your local Claude Code OAuth credentials and uploads the
claudeAiOauth block to kuso, which stores it in the 'kuso-incident-agent-cc'
secret so the agent runs as you. The agent uses YOUR Claude Code subscription.

Credential source (in order):
  --file <path>                          explicit file
  $CLAUDE_CONFIG_DIR/.credentials.json   if CLAUDE_CONFIG_DIR is set
  ~/.claude/.credentials.json            default file
  macOS Keychain ("Claude Code-credentials")  fallback on darwin

This goes through the kuso admin API (settings:admin). To avoid the API and
pipe straight to kubectl instead, use --print-kubectl.`,
	Example: `  kuso incident-agent set-credentials
  kuso incident-agent set-credentials --file ~/creds.json
  kuso incident-agent set-credentials --print-kubectl | sh`,
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, src, err := readClaudeCredentials(incidentAgentCredsFile)
		if err != nil {
			return err
		}
		// Keep only the claudeAiOauth block (strip MCP cruft) + validate.
		clean, err := extractClaudeAiOauth(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}

		if incidentAgentPrintKubectl {
			// Write the clean blob to a temp file path the operator can apply.
			fmt.Fprintf(os.Stderr, "# credentials read from %s\n", src)
			fmt.Printf(
				"printf '%%s' %s | kubectl create secret generic kuso-incident-agent-cc -n %s "+
					"--from-file=credentials.json=/dev/stdin --dry-run=client -o yaml | kubectl apply -f -\n",
				shellQuote(string(clean)), incidentAgentSetCredsNamespace,
			)
			return nil
		}

		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.PutIncidentAgentCCCredentials(string(clean))
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("Claude Code credentials uploaded (from %s).\n", src)
		return nil
	},
}

// claudeCredentialsPath resolves the local Claude Code credentials file,
// honouring $CLAUDE_CONFIG_DIR and falling back to ~/.claude.
func claudeCredentialsPath() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".credentials.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".claude", ".credentials.json"), nil
}

// readClaudeCredentials returns the raw credentials JSON + a human source
// label. Tries (in order): explicit --file, the resolved file path, then the
// macOS Keychain. Returns an actionable error if none yield creds.
func readClaudeCredentials(file string) (raw []byte, src string, err error) {
	if file != "" {
		b, e := os.ReadFile(file)
		if e != nil {
			return nil, "", fmt.Errorf("read %s: %w", file, e)
		}
		return b, file, nil
	}
	path, e := claudeCredentialsPath()
	if e == nil {
		if b, re := os.ReadFile(path); re == nil {
			return b, path, nil
		}
	}
	// macOS: creds live in the Keychain, not a file.
	if runtime.GOOS == "darwin" {
		out, ke := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
		if ke == nil && len(out) > 0 {
			return out, "macOS Keychain", nil
		}
	}
	return nil, "", fmt.Errorf("Claude Code credentials not found (tried %s%s).\n"+
		"Run `claude login` first, or pass --file <path>.", path,
		map[bool]string{true: " + macOS Keychain", false: ""}[runtime.GOOS == "darwin"])
}

// extractClaudeAiOauth validates the blob carries claudeAiOauth.accessToken
// and returns a minimal {claudeAiOauth: ...} JSON (stripping MCP cruft).
func extractClaudeAiOauth(raw []byte) ([]byte, error) {
	var probe struct {
		ClaudeAiOauth json.RawMessage `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	var hasToken struct {
		AccessToken string `json:"accessToken"`
	}
	if len(probe.ClaudeAiOauth) == 0 || json.Unmarshal(probe.ClaudeAiOauth, &hasToken) != nil || hasToken.AccessToken == "" {
		return nil, fmt.Errorf("missing claudeAiOauth.accessToken (is this a Claude Code credentials file?)")
	}
	return json.Marshal(map[string]json.RawMessage{"claudeAiOauth": probe.ClaudeAiOauth})
}

// shellQuote single-quotes a string for safe embedding in a printed shell
// command (the --print-kubectl path).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func incidentTarget(in kusoApi.Incident) string {
	switch {
	case in.Project != "" && in.Service != "":
		return in.Project + "/" + in.Service
	case in.Service != "": // node name for node.unreachable, or unscoped service
		return in.Service
	case in.Project != "":
		return in.Project
	default:
		return "-"
	}
}

func incidentPRCell(in kusoApi.Incident) string {
	if in.PRNumber > 0 {
		return fmt.Sprintf("#%d", in.PRNumber)
	}
	return "-"
}

// incidentAge renders a coarse age (s/m/h/d) from a time.Time.
func incidentAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func init() {
	rootCmd.AddCommand(incidentCmd)
	incidentCmd.AddCommand(incidentListCmd)
	incidentListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	incidentCmd.AddCommand(incidentShowCmd)
	incidentShowCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	incidentCmd.AddCommand(incidentResolveCmd)

	rootCmd.AddCommand(incidentAgentCmd)
	incidentAgentCmd.AddCommand(incidentAgentSetCredsCmd)
	incidentAgentSetCredsCmd.Flags().StringVarP(&incidentAgentSetCredsNamespace, "namespace", "n", "kuso", "namespace for the credentials secret (--print-kubectl only)")
	incidentAgentSetCredsCmd.Flags().StringVar(&incidentAgentCredsFile, "file", "", "explicit credentials file (default: ~/.claude/.credentials.json or macOS Keychain)")
	incidentAgentSetCredsCmd.Flags().BoolVar(&incidentAgentPrintKubectl, "print-kubectl", false, "print a kubectl command instead of uploading via the kuso API")
}
