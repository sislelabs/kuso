package kusoCli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// `kuso build why <project> <service> [buildId]` — explain a failed
// build.
//
// Reuses the existing ListBuilds client method (the build summary API now
// carries a `failureClass` block on failed builds) and surfaces the
// classified failure: a one-line summary plus the remediation (title,
// detail, and the fix rendered as a copy-pasteable block). With no
// buildId it picks the most recent failed build; with one it explains
// that specific build.
//
// This is the human-facing companion to `kuso build list`'s terse REASON
// column — `why` is where you go when the one-liner isn't enough.

// buildRemediation mirrors the `remediation` block on a build's
// failureClass. Fix is the suggested patch/command; FixLang hints the
// syntax for rendering; DocsAnchor points at deeper docs.
type buildRemediation struct {
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Fix        string `json:"fix"`
	FixLang    string `json:"fixLang"`
	DocsAnchor string `json:"docsAnchor"`
}

// buildFailureClass mirrors the failureClass field the build summary API
// attaches to failed builds. Kind is the classifier bucket; Tab/LineHint/
// LineNum point at the offending build-log location.
type buildFailureClass struct {
	Kind        string           `json:"kind"`
	Tab         string           `json:"tab"`
	Summary     string           `json:"summary"`
	LineHint    string           `json:"lineHint"`
	LineNum     int              `json:"lineNum"`
	Remediation buildRemediation `json:"remediation"`
}

// buildWhyRow decodes just the fields `why` needs from each BuildSummary.
type buildWhyRow struct {
	ID           string             `json:"id"`
	Status       string             `json:"status"`
	CommitSha    string             `json:"commitSha"`
	ErrorMessage string             `json:"errorMessage,omitempty"`
	FailureClass *buildFailureClass `json:"failureClass,omitempty"`
}

var buildWhyCmd = &cobra.Command{
	Use:   "why <project> <service> [buildId]",
	Short: "Explain why a build failed (classified failure + remediation)",
	Long: `Fetch a service's builds and explain a failed one: a classified summary
plus the suggested remediation, including a copy-pasteable fix. With no
buildId it picks the most recent failed build; pass a buildId to explain
a specific one.`,
	Example: `  kuso build why analiz api
  kuso build why analiz api build-abc123`,
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service := args[0], args[1]
		var wantID string
		if len(args) == 3 {
			wantID = args[2]
		}
		resp, err := api.ListBuilds(project, service)
		if err != nil {
			return fmt.Errorf("list builds: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var items []buildWhyRow
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode: %w", err)
		}

		// Locate the target build. With an explicit id we match exactly;
		// otherwise we take the first failed build (the API returns
		// newest-first per the handler contract).
		var target *buildWhyRow
		if wantID != "" {
			for i := range items {
				if items[i].ID == wantID {
					target = &items[i]
					break
				}
			}
			if target == nil {
				return fmt.Errorf("build %s not found for %s/%s", wantID, project, service)
			}
		} else {
			for i := range items {
				if isFailedStatus(items[i].Status) {
					target = &items[i]
					break
				}
			}
			if target == nil {
				return fmt.Errorf("no failed builds for %s/%s — nothing to explain", project, service)
			}
		}

		if outputFormat == "json" {
			return jsonOut(target)
		}

		fmt.Printf("build %s (%s)\n", target.ID, target.Status)
		if fc := target.FailureClass; fc != nil {
			if fc.Kind != "" {
				fmt.Printf("class: %s\n", fc.Kind)
			}
			if fc.Summary != "" {
				fmt.Printf("\n%s\n", fc.Summary)
			}
			if fc.LineHint != "" {
				if fc.LineNum > 0 {
					fmt.Printf("  at line %d: %s\n", fc.LineNum, fc.LineHint)
				} else {
					fmt.Printf("  %s\n", fc.LineHint)
				}
			}
			r := fc.Remediation
			if r.Title != "" || r.Detail != "" || r.Fix != "" {
				fmt.Println()
				if r.Title != "" {
					fmt.Printf("Remediation: %s\n", r.Title)
				}
				if r.Detail != "" {
					fmt.Printf("%s\n", r.Detail)
				}
				if r.Fix != "" {
					fmt.Println()
					fmt.Println("  ---")
					for _, line := range splitLines([]byte(r.Fix)) {
						fmt.Printf("  %s\n", line)
					}
					fmt.Println("  ---")
				}
				if r.DocsAnchor != "" {
					fmt.Printf("\ndocs: %s\n", r.DocsAnchor)
				}
			}
			return nil
		}

		// No failureClass — fall back to the raw error message so the
		// command still says something useful on older servers or
		// un-classified failures.
		if target.ErrorMessage != "" {
			fmt.Printf("\n%s\n", target.ErrorMessage)
		} else {
			fmt.Println("\nno failure classification available for this build")
		}
		return nil
	},
}

// isFailedStatus reports whether a build status is a terminal failure.
// Mirrors the set the build poller treats as failed (failed/error).
func isFailedStatus(s string) bool {
	return s == "failed" || s == "error"
}

func init() {
	buildWhyCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	buildCmd.AddCommand(buildWhyCmd)
}
