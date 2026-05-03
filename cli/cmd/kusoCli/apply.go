package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	applyFile   string
	applyDryRun bool
)

func init() {
	applyCmd.Flags().StringVarP(&applyFile, "file", "f", "kuso.yml", "path to the kuso.yml file")
	applyCmd.Flags().BoolVar(&applyDryRun, "dry-run", false, "show the plan without writing")
	rootCmd.AddCommand(applyCmd)
}

// applyCmd reads kuso.yml from disk and POSTs it to
// /api/projects/{p}/apply. The project name is read from the YAML
// itself (project: <name>) — no flag needed. With --dry-run we just
// print the plan; otherwise we apply and print any per-step failures.
var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply kuso.yml to the connected kuso instance.",
	Long: `Reads kuso.yml from the working directory (or --file <path>) and
sends it to the server, which diffs it against the live project and
reconciles. With --dry-run the server returns the plan but doesn't
write anything.`,
	Example: `  kuso apply
  kuso apply -f config/kuso.yml --dry-run`,
	Run: func(cmd *cobra.Command, args []string) {
		body, err := os.ReadFile(applyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", applyFile, err)
			os.Exit(1)
		}

		// Pull project name out of the YAML so we don't make the user
		// repeat themselves on the CLI flag. We treat anything that
		// fails this lookup as an "is this even kuso.yml?" error.
		project := readProjectFromYAML(body)
		if project == "" {
			fmt.Fprintln(os.Stderr, "error: kuso.yml must declare a top-level `project:` field")
			os.Exit(1)
		}

		resp, err := api.Apply(project, body, applyDryRun)
		if err != nil {
			fmt.Fprintln(os.Stderr, "apply:", err)
			os.Exit(1)
		}
		if resp.StatusCode() >= 400 {
			fmt.Fprintf(os.Stderr, "apply failed (%d): %s\n", resp.StatusCode(), resp.String())
			os.Exit(1)
		}
		printApplyResult(resp.Body(), applyDryRun)
	},
}

// readProjectFromYAML pulls the `project:` field without parsing the
// whole structure. We could call yaml.Unmarshal here but importing
// the full schema package into the CLI binary is overkill for one
// scalar — line scan is enough.
func readProjectFromYAML(body []byte) string {
	for _, line := range splitLines(body) {
		line = trimAny(line, " \t")
		if hasPrefixA(line, "project:") {
			return trimQuotes(trimAny(line[len("project:"):], " \t"))
		}
	}
	return ""
}

func printApplyResult(body []byte, dryRun bool) {
	// Plan-only response on dry run; ApplyResult on real apply. Try
	// the latter first; fall back to plan-only when there's no `plan`
	// wrapper.
	var ar struct {
		Plan struct {
			ServicesToCreate []string `json:"servicesToCreate"`
			ServicesToUpdate []string `json:"servicesToUpdate"`
			ServicesToDelete []string `json:"servicesToDelete"`
			AddonsToCreate   []string `json:"addonsToCreate"`
			AddonsToUpdate   []string `json:"addonsToUpdate"`
			AddonsToDelete   []string `json:"addonsToDelete"`
		} `json:"plan"`
		Errors []struct {
			Resource string `json:"resource"`
			Op       string `json:"op"`
			Message  string `json:"message"`
		} `json:"errors"`
	}
	if dryRun {
		_ = json.Unmarshal(body, &ar.Plan)
	} else {
		_ = json.Unmarshal(body, &ar)
	}
	p := ar.Plan
	verb := "would"
	if !dryRun {
		verb = "did"
	}
	fmt.Printf("services: %s create %d, update %d, delete %d\n",
		verb, len(p.ServicesToCreate), len(p.ServicesToUpdate), len(p.ServicesToDelete))
	fmt.Printf("addons:   %s create %d, update %d, delete %d\n",
		verb, len(p.AddonsToCreate), len(p.AddonsToUpdate), len(p.AddonsToDelete))
	for _, n := range p.ServicesToCreate {
		fmt.Println("  + service", n)
	}
	for _, n := range p.ServicesToUpdate {
		fmt.Println("  ~ service", n)
	}
	for _, n := range p.ServicesToDelete {
		fmt.Println("  - service", n)
	}
	for _, n := range p.AddonsToCreate {
		fmt.Println("  + addon", n)
	}
	for _, n := range p.AddonsToDelete {
		fmt.Println("  - addon", n)
	}
	if len(ar.Errors) > 0 {
		fmt.Fprintln(os.Stderr, "\nERRORS:")
		for _, e := range ar.Errors {
			fmt.Fprintf(os.Stderr, "  %s %s: %s\n", e.Op, e.Resource, e.Message)
		}
		os.Exit(1)
	}
}

// Tiny string helpers — kept here so apply.go doesn't pull in a
// utility package that the rest of the legacy CLI doesn't need.
func splitLines(b []byte) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, string(b[start:]))
	}
	return out
}

func trimAny(s, cut string) string {
	for len(s) > 0 {
		drop := false
		for _, c := range cut {
			if rune(s[0]) == c {
				s = s[1:]
				drop = true
				break
			}
		}
		if !drop {
			return s
		}
	}
	return s
}

func hasPrefixA(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func trimQuotes(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
