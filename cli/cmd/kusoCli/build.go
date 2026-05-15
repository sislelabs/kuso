package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// indexNewline returns the index of the first '\n' or '\r' in s, or -1.
// Used to clip multi-line failure reasons down to their first line for
// table rendering — the full text is still available via `-o json`.
func indexNewline(s string) int {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return i
	}
	return -1
}

// `kuso build` — trigger and inspect builds.
//
//   kuso build trigger <project> <service> [--branch main]
//   kuso build list <project> <service> [-o json]
//
// `kuso redeploy <project> <service>` is the same as `build trigger` —
// kept as an alias because that's the verb people reach for.

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Trigger and inspect builds",
}

var (
	buildTriggerBranch string
	buildTriggerRef    string
)

var buildTriggerCmd = &cobra.Command{
	Use:     "trigger <project> <service>",
	Aliases: []string{"redeploy", "deploy"},
	Short:   "Trigger a build for a service (defaults to the project's default branch)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req := kusoApi.CreateBuildRequest{Branch: buildTriggerBranch, Ref: buildTriggerRef}
		resp, err := api.CreateBuild(args[0], args[1], req)
		if err != nil {
			return fmt.Errorf("trigger build: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		// Server returns the BuildSummary wire shape (flat
		// {id,serviceName,branch,commitSha,imageTag,status}), NOT the
		// raw KusoBuild CR. Earlier versions of this command decoded it
		// as a CR and printed an empty name; switch to the typed shape
		// the handler actually emits.
		var data struct {
			ID     string `json:"id"`
			Branch string `json:"branch"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(resp.Body(), &data)
		fmt.Printf("build %s started (branch=%s, status=%s)\n", data.ID, data.Branch, data.Status)
		return nil
	},
}

var buildListCmd = &cobra.Command{
	Use:     "list <project> <service>",
	Aliases: []string{"ls"},
	Short:   "List recent builds for a service (newest first)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListBuilds(args[0], args[1])
		if err != nil {
			return fmt.Errorf("list builds: %w", err)
		}
		// Server returns []BuildSummary (flat wire shape). The old code
		// decoded as []KusoBuild and printed an empty table because
		// metadata/spec/status were never populated.
		type buildRow struct {
			ID           string `json:"id"`
			Branch       string `json:"branch"`
			CommitSha    string `json:"commitSha"`
			ImageTag     string `json:"imageTag"`
			Status       string `json:"status"`
			StartedAt    string `json:"startedAt"`
			FinishedAt   string `json:"finishedAt"`
			ErrorMessage string `json:"errorMessage,omitempty"`
		}
		var items []buildRow
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		// API already returns newest-first per the handler contract;
		// re-sort defensively on startedAt so manual rows from the
		// future-self CLI are still in the right order.
		sort.SliceStable(items, func(i, j int) bool {
			return items[i].StartedAt > items[j].StartedAt
		})
		switch outputFormat {
		case "json":
			return jsonOut(items)
		case "table", "":
			// Empty list: print a one-line "no builds" so polling scripts
			// can grep `^no builds` to detect the empty case instead of
			// trying to parse an empty-body table. Returning the
			// header-only frame was scriptable but ugly.
			if len(items) == 0 {
				fmt.Println("no builds yet — try `kuso build trigger <project> <service>`")
				return nil
			}
			// Add a REASON column only when at least one row has a failure
			// message — keeps successful-only listings narrow on small terms,
			// surfaces the actual cause when a build's failed so users don't
			// have to ssh to the cluster to find out why.
			showReason := false
			for _, b := range items {
				if b.ErrorMessage != "" {
					showReason = true
					break
				}
			}
			t := tablewriter.NewWriter(os.Stdout)
			header := []string{"ID", "BRANCH", "SHA", "TAG", "STATUS", "AGE"}
			if showReason {
				header = append(header, "REASON")
			}
			t.SetHeader(header)
			for _, b := range items {
				sha := b.CommitSha
				if len(sha) > 12 {
					sha = sha[:12]
				}
				row := []string{
					b.ID,
					b.Branch,
					sha,
					b.ImageTag,
					b.Status,
					relativeAge(b.StartedAt),
				}
				if showReason {
					reason := b.ErrorMessage
					// Cap to one line; the full text is in `-o json` for
					// scripts and in the archived build log for humans.
					if i := indexNewline(reason); i >= 0 {
						reason = reason[:i]
					}
					if len(reason) > 80 {
						reason = reason[:77] + "..."
					}
					row = append(row, reason)
				}
				t.Append(row)
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

// relativeAge converts an ISO8601 timestamp to "<n>m" / "<n>h" / "<n>d".
func relativeAge(iso string) string {
	if iso == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
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
	rootCmd.AddCommand(buildCmd)

	buildCmd.AddCommand(buildTriggerCmd)
	buildTriggerCmd.Flags().StringVar(&buildTriggerBranch, "branch", "", "branch to build (default: project default branch)")
	buildTriggerCmd.Flags().StringVar(&buildTriggerRef, "ref", "", "specific commit SHA to build")

	buildCmd.AddCommand(buildListCmd)
	buildListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")

	rollbackCmd := &cobra.Command{
		Use:   "rollback <project> <service> <build>",
		Short: "Re-point production at a previous successful build's image",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			resp, err := api.RollbackBuild(args[0], args[1], args[2])
			if err != nil {
				return err
			}
			if resp.StatusCode() >= 300 {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
			}
			fmt.Printf("rolled %s/%s back to build %s\n", args[0], args[1], args[2])
			return nil
		},
	}
	buildCmd.AddCommand(rollbackCmd)

	cancelCmd := &cobra.Command{
		Use:   "cancel <project> <service> <build>",
		Short: "Stop an in-flight build",
		Long: "Stop a running or pending build. The build CR is preserved with " +
			"phase=cancelled so it stays visible in `kuso build list`. Returns " +
			"409 when the build already reached a terminal phase.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			resp, err := api.CancelBuild(args[0], args[1], args[2])
			if err != nil {
				return fmt.Errorf("cancel build: %w", err)
			}
			if resp.StatusCode() >= 300 {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
			}
			fmt.Printf("build %s cancelled\n", args[2])
			return nil
		},
	}
	buildCmd.AddCommand(cancelCmd)

	// `kuso redeploy <project> <service>` shortcut at top level.
	redeployCmd := &cobra.Command{
		Use:     "redeploy <project> <service>",
		Short:   "Trigger a fresh build + deploy of a service",
		Args:    cobra.ExactArgs(2),
		RunE:    buildTriggerCmd.RunE,
	}
	redeployCmd.Flags().StringVar(&buildTriggerBranch, "branch", "", "branch to deploy")
	redeployCmd.Flags().StringVar(&buildTriggerRef, "ref", "", "specific commit SHA")
	rootCmd.AddCommand(redeployCmd)
}
