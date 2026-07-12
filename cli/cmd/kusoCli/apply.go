package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	applyFile          string
	applyDryRun        bool
	applyRotateSecrets bool
)

func init() {
	applyCmd.Flags().StringVarP(&applyFile, "file", "f", "kuso.yaml", "path to the kuso.yaml file")
	applyCmd.Flags().BoolVar(&applyDryRun, "dry-run", false, "show the plan without writing")
	applyCmd.Flags().BoolVar(&applyRotateSecrets, "rotate-secrets", false, "re-mint generated ({generate: …}) secrets even if they already exist (default: generate-once)")
	rootCmd.AddCommand(applyCmd)
}

// applyCmd reads kuso.yml from disk and POSTs it to
// /api/projects/{p}/apply. The project name is read from the YAML
// itself (project: <name>) — no flag needed. With --dry-run we just
// print the plan; otherwise we apply and print any per-step failures.
var applyCmd = &cobra.Command{
	Use:   "apply [file]",
	Short: "Apply kuso.yml to the connected kuso instance.",
	Long: `Reads kuso.yml from the working directory (or the [file] positional
arg, or --file <path>) and sends it to the server, which diffs it
against the live project and reconciles. With --dry-run the server
returns the plan but doesn't write anything.

The file's "project:" field selects the target project — no flag
needed.`,
	Example: `  kuso apply
  kuso apply kuso.yaml --dry-run
  kuso apply -f config/kuso.yml --dry-run`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		path := applyFile
		explicit := cmd.Flags().Changed("file")
		if len(args) == 1 {
			path = args[0]
			explicit = true
		}
		// When the user didn't explicitly pick a file and the default
		// kuso.yaml is absent, fall back to the legacy kuso.yml name
		// before erroring.
		if !explicit {
			if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
				if _, altErr := os.Stat("kuso.yml"); altErr == nil {
					path = "kuso.yml"
				}
			}
		}
		body, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
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

		resp, err := api.ApplyConfig(project, body, applyDryRun, applyRotateSecrets)
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
			val := trimAny(line[len("project:"):], " \t")
			val = stripInlineComment(val)
			return trimQuotes(trimAny(val, " \t"))
		}
	}
	return ""
}

// stripInlineComment removes a trailing YAML `# comment` from a scalar
// value so `project: foo # note` yields "foo", not "foo # note". A '#'
// inside single/double quotes is left intact (it's part of the value).
// This is the line-scan equivalent of what a real YAML parser does; the
// full parse is deliberately avoided here (see readProjectFromYAML).
func stripInlineComment(s string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			// A comment must be preceded by whitespace (or start the
			// value) per YAML — "a#b" is a literal, "a #b" is a comment.
			if !inSingle && !inDouble && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
				return trimTrailing(s[:i], " \t")
			}
		}
	}
	return s
}

// trimTrailing drops trailing runs of any byte in cut (trimAny only
// trims leading).
func trimTrailing(s, cut string) string {
	for len(s) > 0 {
		last := s[len(s)-1]
		drop := false
		for i := 0; i < len(cut); i++ {
			if last == cut[i] {
				s = s[:len(s)-1]
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
			CronsToCreate    []string `json:"cronsToCreate"`
			CronsToUpdate    []string `json:"cronsToUpdate"`
			CronsToDelete    []string `json:"cronsToDelete"`
			WouldDelete      []string `json:"wouldDelete"`
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
	fmt.Printf("crons:    %s create %d, update %d, delete %d\n",
		verb, len(p.CronsToCreate), len(p.CronsToUpdate), len(p.CronsToDelete))
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
	for _, n := range p.AddonsToUpdate {
		fmt.Println("  ~ addon", n)
	}
	for _, n := range p.AddonsToDelete {
		fmt.Println("  - addon", n)
	}
	for _, n := range p.CronsToCreate {
		fmt.Println("  + cron", n)
	}
	for _, n := range p.CronsToUpdate {
		fmt.Println("  ~ cron", n)
	}
	for _, n := range p.CronsToDelete {
		fmt.Println("  - cron", n)
	}
	if len(p.WouldDelete) > 0 {
		fmt.Printf("\nnot pruned (set `prune: true` to delete): %d\n", len(p.WouldDelete))
		for _, n := range p.WouldDelete {
			fmt.Println("  ! " + n)
		}
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
