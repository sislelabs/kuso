package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

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
			ID         string `json:"id"`
			Branch     string `json:"branch"`
			CommitSha  string `json:"commitSha"`
			ImageTag   string `json:"imageTag"`
			Status     string `json:"status"`
			StartedAt  string `json:"startedAt"`
			FinishedAt string `json:"finishedAt"`
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
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "BRANCH", "SHA", "TAG", "STATUS", "AGE"})
			for _, b := range items {
				sha := b.CommitSha
				if len(sha) > 12 {
					sha = sha[:12]
				}
				t.Append([]string{
					b.ID,
					b.Branch,
					sha,
					b.ImageTag,
					b.Status,
					relativeAge(b.StartedAt),
				})
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
