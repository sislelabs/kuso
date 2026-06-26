package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// `kuso health` — platform-trust reconcile.
//
//   kuso health [-o table|json]
//   kuso health fix <resource> [--action <action>] [-y]
//
// `health` runs the read-only reconcile scan and prints the flagged
// issues (drifted/unsafe resources) with a severity rollup footer. When
// an issue carries a one-shot `fix` hint, it's printed indented under the
// row so the operator has a copy-pasteable next step.
//
// `health fix` applies the server's suggested remediation for a single
// issue. It mutates live infra, so it prompts for confirmation unless
// -y/--yes is given (or stdin isn't a TTY, matching the other
// destructive commands).

// healthIssue mirrors one entry in the reconcile scan's `issues` array.
// Severity is one of critical|warning|info. Fix is an optional one-shot
// command hint; RunbookCmd points at deeper docs.
type healthIssue struct {
	Resource   string `json:"resource"`
	Namespace  string `json:"namespace"`
	Project    string `json:"project"`
	Type       string `json:"type"`
	AddonKind  string `json:"addonKind"`
	Kind       string `json:"kind"`
	Severity   string `json:"severity"`
	Summary    string `json:"summary"`
	Detail     string `json:"detail"`
	Action     string `json:"action"`
	Safe       bool   `json:"safe"`
	Fix        string `json:"fix"`
	RunbookCmd string `json:"runbookCmd"`
}

// healthReport is the full reconcile scan response.
type healthReport struct {
	Issues   []healthIssue `json:"issues"`
	Healthy  int           `json:"healthy"`
	Scanned  int           `json:"scanned"`
	Critical int           `json:"critical"`
	Warning  int           `json:"warning"`
	Info     int           `json:"info"`
}

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Run the platform-trust reconcile scan over cluster resources",
	Long: `Scan cluster resources for drift and unsafe configuration and report
any issues, grouped by severity. Read-only — nothing is changed. Use
'kuso health fix <resource>' to apply a flagged issue's remediation.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ReconcileHealth()
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		if outputFormat == "json" {
			fmt.Println(string(resp.Body()))
			return nil
		}
		var rep healthReport
		if err := json.Unmarshal(resp.Body(), &rep); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if len(rep.Issues) == 0 {
			fmt.Printf("No issues. %d healthy / %d scanned.\n", rep.Healthy, rep.Scanned)
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"SEVERITY", "RESOURCE", "KIND", "SUMMARY"})
		for _, is := range rep.Issues {
			tw.Append([]string{
				is.Severity,
				is.Resource,
				dashIfEmpty(is.Kind),
				is.Summary,
			})
		}
		tw.Render()
		// Surface one-shot fix hints under the table — the tablewriter
		// frame can't hold a multi-cell continuation, so print them as
		// indented follow-on lines keyed by resource.
		for _, is := range rep.Issues {
			if is.Fix != "" {
				fmt.Printf("  fix %s: %s\n", is.Resource, is.Fix)
			}
		}
		fmt.Printf("\n%d healthy / %d scanned · %d critical %d warning %d info\n",
			rep.Healthy, rep.Scanned, rep.Critical, rep.Warning, rep.Info)
		return nil
	},
}

var healthFixAction string
var healthFixYes bool

var healthFixCmd = &cobra.Command{
	Use:   "fix <resource>",
	Short: "Apply the reconcile remediation for a single flagged resource",
	Long: `Apply the server's suggested remediation for one reconcile issue. This
mutates live infra. Without --action the server re-scans to resolve the
issue's canonical action itself. Prompts for confirmation unless --yes.`,
	Example: `  kuso health fix myproject/postgres-conn
  kuso health fix myproject/postgres-conn --action recreate-secret -y`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resource := args[0]
		prompt := fmt.Sprintf("Remediate %s", resource)
		if healthFixAction != "" {
			prompt += fmt.Sprintf(" (action=%s)", healthFixAction)
		}
		prompt += "? This mutates live infrastructure."
		if err := confirmDestructive(healthFixYes, prompt); err != nil {
			return err
		}
		resp, err := api.Remediate(resource, healthFixAction)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var out struct {
			Resource string `json:"resource"`
			Action   string `json:"action"`
			Applied  bool   `json:"applied"`
			Message  string `json:"message"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		msg := out.Message
		if msg == "" {
			if out.Applied {
				msg = "remediation applied"
			} else {
				msg = "no change"
			}
		}
		fmt.Printf("%s: %s\n", out.Resource, msg)
		return nil
	},
}

func init() {
	healthCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	healthFixCmd.Flags().StringVar(&healthFixAction, "action", "", "remediation action (default: server resolves it)")
	healthFixCmd.Flags().BoolVarP(&healthFixYes, "yes", "y", false, "skip the confirmation prompt")
	healthCmd.AddCommand(healthFixCmd)
	rootCmd.AddCommand(healthCmd)
}
