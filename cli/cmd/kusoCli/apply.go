package kusoCli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	applyFile          string
	applyDryRun        bool
	applyRotateSecrets bool
	applyYes           bool
)

func init() {
	applyCmd.Flags().StringVarP(&applyFile, "file", "f", "", "path to the manifest (default: kuso.yml, or kuso.yaml if only that exists)")
	applyCmd.Flags().BoolVar(&applyDryRun, "dry-run", false, "show the plan without writing")
	applyCmd.Flags().BoolVar(&applyRotateSecrets, "rotate-secrets", false, "re-mint generated ({generate: …}) secrets even if they already exist (default: generate-once)")
	applyCmd.Flags().BoolVarP(&applyYes, "yes", "y", false, "skip the confirmation when the plan deletes services, addons or crons (prune: true)")
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
needed.

With "prune: true" in the file, services, addons and crons missing from
it are deleted (addon data is not recoverable). When the plan contains
deletions, apply prints the plan and asks for confirmation first; pass
--yes to skip the prompt (required when stdin is not a terminal).`,
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
		// No explicit file: resolve via the shared helper so apply,
		// status, and init all agree — kuso.yml preferred (the name
		// `kuso init` writes), kuso.yaml accepted, a note when both
		// exist.
		if !explicit {
			resolved, note, rerr := resolveManifestPath(".")
			if rerr != nil {
				fmt.Fprintln(os.Stderr, "error:", rerr)
				os.Exit(1)
			}
			if note != "" {
				fmt.Fprintln(os.Stderr, "note:", note)
			}
			path = resolved
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

		if !applyDryRun && !applyYes {
			if err := confirmApplyDeletes(project, body); err != nil {
				fmt.Fprintln(os.Stderr, "apply:", err)
				os.Exit(1)
			}
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

// confirmApplyDeletes fetches the server's plan for body and, when it
// deletes anything (prune: true), prints the plan and asks before the
// real apply. Plans without deletions proceed without a prompt.
func confirmApplyDeletes(project string, body []byte) error {
	resp, err := api.ApplyConfig(project, body, true, false)
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if resp.StatusCode() >= 400 {
		return fmt.Errorf("plan failed (%d): %s", resp.StatusCode(), resp.String())
	}
	var plan applyPlan
	if err := json.Unmarshal(resp.Body(), &plan); err != nil {
		return fmt.Errorf("decode plan: %w", err)
	}
	deletes := applyPlanDeletes(plan)
	if len(deletes) == 0 {
		return nil
	}
	renderApplyResult(os.Stderr, os.Stderr, resp.Body(), true)
	return confirmDestructive(false, fmt.Sprintf(
		"\nprune: true will DELETE %d resource(s) from project %s: %s. Addon data is not recoverable. Continue?",
		len(deletes), project, strings.Join(deletes, ", ")))
}

func applyPlanDeletes(p applyPlan) []string {
	var out []string
	for _, n := range p.ServicesToDelete {
		out = append(out, "service:"+n)
	}
	for _, n := range p.AddonsToDelete {
		out = append(out, "addon:"+n)
	}
	for _, n := range p.CronsToDelete {
		out = append(out, "cron:"+n)
	}
	return out
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
	if renderApplyResult(os.Stdout, os.Stderr, body, dryRun) {
		os.Exit(1)
	}
}

type applyFieldChange struct {
	Field       string `json:"field"`
	From        string `json:"from"`
	To          string `json:"to"`
	Destructive bool   `json:"destructive"`
}

type applyResourceChange struct {
	Resource   string             `json:"resource"`
	Fields     []applyFieldChange `json:"fields"`
	NotApplied bool               `json:"notApplied"`
}

type applyPlan struct {
	ServicesToCreate  []string              `json:"servicesToCreate"`
	ServicesToUpdate  []string              `json:"servicesToUpdate"`
	ServicesToDelete  []string              `json:"servicesToDelete"`
	ServicesUnchanged []string              `json:"servicesUnchanged"`
	AddonsToCreate    []string              `json:"addonsToCreate"`
	AddonsToUpdate    []string              `json:"addonsToUpdate"`
	AddonsToDelete    []string              `json:"addonsToDelete"`
	AddonsUnchanged   []string              `json:"addonsUnchanged"`
	CronsToCreate     []string              `json:"cronsToCreate"`
	CronsToUpdate     []string              `json:"cronsToUpdate"`
	CronsToDelete     []string              `json:"cronsToDelete"`
	CronsUnchanged    []string              `json:"cronsUnchanged"`
	WouldDelete       []string              `json:"wouldDelete"`
	Changes           []applyResourceChange `json:"changes"`
	Warnings          []string              `json:"warnings"`
}

// renderApplyResult prints the plan (dry run) or ApplyResult (real
// apply) with per-field diffs. Returns true when the result carries
// step errors.
func renderApplyResult(out, errOut io.Writer, body []byte, dryRun bool) bool {
	var ar struct {
		Plan   applyPlan `json:"plan"`
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
	fmt.Fprintf(out, "services: %s create %d, update %d, delete %d, unchanged %d\n",
		verb, len(p.ServicesToCreate), len(p.ServicesToUpdate), len(p.ServicesToDelete), len(p.ServicesUnchanged))
	fmt.Fprintf(out, "addons:   %s create %d, update %d, delete %d, unchanged %d\n",
		verb, len(p.AddonsToCreate), len(p.AddonsToUpdate), len(p.AddonsToDelete), len(p.AddonsUnchanged))
	fmt.Fprintf(out, "crons:    %s create %d, update %d, delete %d, unchanged %d\n",
		verb, len(p.CronsToCreate), len(p.CronsToUpdate), len(p.CronsToDelete), len(p.CronsUnchanged))

	changes := map[string]applyResourceChange{}
	destructive := 0
	for _, c := range p.Changes {
		if !c.NotApplied {
			changes[c.Resource] = c
			for _, f := range c.Fields {
				if f.Destructive {
					destructive++
				}
			}
		}
	}
	printFields := func(fields []applyFieldChange) {
		for _, f := range fields {
			mark := ""
			if f.Destructive {
				mark = "  [DESTRUCTIVE]"
			}
			fmt.Fprintf(out, "      %s: %s → %s%s\n", f.Field, f.From, f.To, mark)
		}
	}
	section := func(sign, kind string, names []string) {
		for _, n := range names {
			fmt.Fprintf(out, "  %s %s %s\n", sign, kind, n)
			if c, ok := changes[kind+":"+n]; ok {
				printFields(c.Fields)
			}
		}
	}
	section("+", "service", p.ServicesToCreate)
	section("~", "service", p.ServicesToUpdate)
	section("-", "service", p.ServicesToDelete)
	section("+", "addon", p.AddonsToCreate)
	section("~", "addon", p.AddonsToUpdate)
	section("-", "addon", p.AddonsToDelete)
	section("+", "cron", p.CronsToCreate)
	section("~", "cron", p.CronsToUpdate)
	section("-", "cron", p.CronsToDelete)

	for _, c := range p.Changes {
		if !c.NotApplied {
			continue
		}
		kind, name, _ := strings.Cut(c.Resource, ":")
		fmt.Fprintf(out, "  ! %s %s (drift, not applied — apply doesn't modify this)\n", kind, name)
		printFields(c.Fields)
	}
	if destructive > 0 {
		s := "s"
		if destructive == 1 {
			s = ""
		}
		fmt.Fprintf(out, "\nWARNING: %d destructive change%s (marked [DESTRUCTIVE] above)\n", destructive, s)
	}
	if len(p.Warnings) > 0 {
		fmt.Fprintln(out, "\nwarnings:")
		for _, w := range p.Warnings {
			fmt.Fprintln(out, "  ! "+w)
		}
	}
	if len(p.WouldDelete) > 0 {
		fmt.Fprintf(out, "\nnot pruned (set `prune: true` to delete): %d\n", len(p.WouldDelete))
		for _, n := range p.WouldDelete {
			fmt.Fprintln(out, "  ! "+n)
		}
	}
	if len(ar.Errors) > 0 {
		fmt.Fprintln(errOut, "\nERRORS:")
		for _, e := range ar.Errors {
			fmt.Fprintf(errOut, "  %s %s: %s\n", e.Op, e.Resource, e.Message)
		}
		return true
	}
	return false
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
