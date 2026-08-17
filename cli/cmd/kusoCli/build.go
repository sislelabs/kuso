package kusoCli

import (
	"encoding/json"
	"fmt"
	"net/url"
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
	buildTriggerDryRun bool
	buildTriggerFollow bool
)

// pollBuildToTerminal polls ListBuilds until the build with id buildID
// reaches a terminal status (succeeded/failed/cancelled), printing a
// status line on each change. Returns the terminal status. Mirrors the
// restore/migrate streaming UX so `build trigger --follow` gives a real
// "did it work" signal instead of fire-and-forget.
func pollBuildToTerminal(project, service, buildID string) (string, error) {
	last := ""
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		resp, err := api.ListBuilds(project, service)
		if err := checkRespErr(resp, err); err != nil {
			return "", fmt.Errorf("poll builds: %w", err)
		}
		var items []struct {
			ID           string `json:"id"`
			Status       string `json:"status"`
			ErrorMessage string `json:"errorMessage,omitempty"`
		}
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return "", fmt.Errorf("decode builds: %w", err)
		}
		for _, b := range items {
			if b.ID != buildID {
				continue
			}
			if b.Status != last {
				fmt.Printf("  build %s: %s\n", buildID, b.Status)
				last = b.Status
			}
			switch b.Status {
			case "succeeded":
				return b.Status, nil
			case "failed", "error", "cancelled":
				if b.ErrorMessage != "" {
					return b.Status, fmt.Errorf("build %s: %s", b.Status, b.ErrorMessage)
				}
				return b.Status, fmt.Errorf("build %s", b.Status)
			case "release-failed":
				// The image built + pushed fine, but the release hook
				// (migration) failed, so the env was NOT promoted — the
				// last GREEN build stays live. This IS terminal: without
				// this arm --follow polled the full 20m deadline waiting
				// for a status that never changes. Return non-zero so CI
				// catches the failed migration.
				if b.ErrorMessage != "" {
					return b.Status, fmt.Errorf("build %s: %s", b.Status, b.ErrorMessage)
				}
				return b.Status, fmt.Errorf("build %s (release hook failed; image not promoted)", b.Status)
			}
		}
		time.Sleep(5 * time.Second)
	}
	return "", fmt.Errorf("build %s did not finish within 20m — check `kuso build list %s %s`", buildID, project, service)
}

var buildTriggerCmd = &cobra.Command{
	Use:     "trigger <project> <service>",
	Aliases: []string{"redeploy", "deploy"},
	Short:   "Trigger a build for a service (defaults to the project's default branch)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req := kusoApi.CreateBuildRequest{
			Branch: buildTriggerBranch,
			Ref:    buildTriggerRef,
			DryRun: buildTriggerDryRun,
		}
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
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("build %s started (branch=%s, status=%s)\n", data.ID, data.Branch, data.Status)
		if buildTriggerFollow && !buildTriggerDryRun && data.ID != "" {
			status, ferr := pollBuildToTerminal(args[0], args[1], data.ID)
			if ferr != nil {
				return ferr // non-zero exit on failed/timeout so CI/scripts catch it
			}
			fmt.Printf("build %s %s\n", data.ID, status)
		}
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
		if err := checkRespErr(resp, err); err != nil {
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
			// QueuePosition is the 1-based place in the cluster-wide
			// build queue; only set while status=queued.
			QueuePosition int `json:"queuePosition,omitempty"`
			// PromoteHold: the atomic same-repo promotion gate's hold
			// reason — Job green, image not rolled until every sibling
			// build of this commit is green. Set only while held.
			PromoteHold string `json:"promoteHold,omitempty"`
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
				if b.ErrorMessage != "" || b.PromoteHold != "" {
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
				status := b.Status
				if b.Status == "queued" && b.QueuePosition > 0 {
					status = fmt.Sprintf("queued (#%d)", b.QueuePosition)
				}
				if b.PromoteHold != "" && (b.Status == "running" || b.Status == "pending") {
					// Only rewrite non-terminal rows: a stale annotation on
					// a terminal build must not masquerade as held.
					status = "held"
				}
				row := []string{
					b.ID,
					b.Branch,
					sha,
					b.ImageTag,
					status,
					relativeAge(b.StartedAt),
				}
				if showReason {
					reason := b.ErrorMessage
					if reason == "" && b.PromoteHold != "" {
						reason = b.PromoteHold
					}
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

// buildLatestRows decodes the /builds/latest payload (map keyed by
// service short-name → build summary) into sorted table rows of
// {service, id, branch, sha12, tag, status, age}. ok=false means the
// body wasn't that shape and the caller should fall back to JSON so a
// differently-shaped response is never silently dropped. The service
// key — the thing that makes the map useful — becomes the first column,
// which is why `-o table` is renderable here at all.
func buildLatestRows(body []byte) ([][]string, bool) {
	var m map[string]struct {
		ID        string `json:"id"`
		Branch    string `json:"branch"`
		CommitSha string `json:"commitSha"`
		ImageTag  string `json:"imageTag"`
		Status    string `json:"status"`
		StartedAt string `json:"startedAt"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, false
	}
	services := make([]string, 0, len(m))
	for s := range m {
		services = append(services, s)
	}
	sort.Strings(services)
	rows := make([][]string, 0, len(m))
	for _, s := range services {
		b := m[s]
		sha := b.CommitSha
		if len(sha) > 12 {
			sha = sha[:12]
		}
		rows = append(rows, []string{s, b.ID, b.Branch, sha, b.ImageTag, b.Status, relativeAge(b.StartedAt)})
	}
	return rows, true
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
	buildTriggerCmd.Flags().BoolVar(&buildTriggerDryRun, "dry-run", false, "compile + assemble image but skip push and env promotion")
	buildTriggerCmd.Flags().BoolVarP(&buildTriggerFollow, "follow", "f", false, "block until the build reaches a terminal state; non-zero exit on failure")

	buildCmd.AddCommand(buildListCmd)
	buildListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")

	// `kuso build latest <project>` — the newest build per service in a
	// project. Server returns a map keyed by service short-name →
	// build summary; `--env` filters to an env-group's branch.
	var buildLatestEnv string
	buildLatestCmd := &cobra.Command{
		Use:   "latest <project>",
		Short: "Show the latest build per service in a project",
		Long: `Return the newest build for each service in a project (map keyed by
service short-name). Pass --env to filter to an env-group's branch
(e.g. production, staging, preview-pr-7); omit it for the newest build
per service regardless of branch.`,
		Args: cobra.ExactArgs(1),
		Example: `  kuso build latest scubatony -o json
  kuso build latest scubatony --env production`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			path := "/api/projects/" + url.PathEscape(args[0]) + "/builds/latest"
			if buildLatestEnv != "" {
				path += "?env=" + url.PathEscape(buildLatestEnv)
			}
			resp, err := api.RawGet(path)
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("build latest: %w", err)
			}
			// Server returns map[service]buildSummary.
			switch outputFormat {
			case "json":
				var out map[string]any
				if err := json.Unmarshal(resp.Body(), &out); err != nil {
					return fmt.Errorf("decode response: %w", err)
				}
				return jsonOut(out)
			case "table", "":
				rows, ok := buildLatestRows(resp.Body())
				if !ok {
					// Unexpected shape — emit JSON rather than silently
					// dropping data (same fallback rule as `db sql`).
					var out any
					if err := json.Unmarshal(resp.Body(), &out); err != nil {
						return fmt.Errorf("decode response: %w", err)
					}
					return jsonOut(out)
				}
				if len(rows) == 0 {
					fmt.Println("no builds yet — try `kuso build trigger <project> <service>`")
					return nil
				}
				t := tablewriter.NewWriter(os.Stdout)
				t.SetHeader([]string{"SERVICE", "ID", "BRANCH", "SHA", "TAG", "STATUS", "AGE"})
				for _, r := range rows {
					t.Append(r)
				}
				t.Render()
				return nil
			default:
				return fmt.Errorf("unsupported output format %q", outputFormat)
			}
		},
	}
	buildLatestCmd.Flags().StringVar(&buildLatestEnv, "env", "", "env-group filter (production, staging, preview-pr-N)")
	// Its own Example documents `-o json`, which errored with
	// "unknown shorthand flag" because the flag was never registered.
	buildLatestCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	buildCmd.AddCommand(buildLatestCmd)

	var buildRollbackEnv string
	rollbackCmd := &cobra.Command{
		Use:   "rollback <project> <service> <build>",
		Short: "Re-point an environment at a previous successful build's image",
		Long: `Re-point an environment at a previous successful build's image.

Defaults to the production env. Pass --env to roll back a named env
(staging, qa, preview-pr-N) instead — without it a rollback aimed at
staging would silently roll PRODUCTION back.`,
		Example: `  kuso build rollback tickero api tickero-api-3abf9b99
  kuso build rollback tickero api tickero-api-3abf9b99 --env staging`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			resp, err := api.RollbackBuild(args[0], args[1], args[2], buildRollbackEnv)
			if err != nil {
				return err
			}
			if resp.StatusCode() >= 300 {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
			}
			target := buildRollbackEnv
			if target == "" {
				target = "production"
			}
			fmt.Printf("rolled %s/%s (%s) back to build %s\n", args[0], args[1], target, args[2])
			return nil
		},
	}
	rollbackCmd.Flags().StringVar(&buildRollbackEnv, "env", "", "environment to roll back (default production)")
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
		Use:   "redeploy <project> <service>",
		Short: "Trigger a fresh build + deploy of a service",
		Args:  cobra.ExactArgs(2),
		RunE:  buildTriggerCmd.RunE,
	}
	// redeploy reuses buildTriggerCmd.RunE verbatim, so it must bind
	// EVERY flag that RunE reads — it reads buildTriggerDryRun and
	// buildTriggerFollow too. Binding only branch/ref made
	// `kuso redeploy p api --follow` fail with "unknown flag" despite
	// the two commands being documented as equivalent.
	redeployCmd.Flags().StringVar(&buildTriggerBranch, "branch", "", "branch to deploy")
	redeployCmd.Flags().StringVar(&buildTriggerRef, "ref", "", "specific commit SHA")
	redeployCmd.Flags().BoolVar(&buildTriggerDryRun, "dry-run", false, "resolve the ref and print what would build, without creating a build")
	redeployCmd.Flags().BoolVar(&buildTriggerFollow, "follow", false, "stream build logs until the build finishes")
	rootCmd.AddCommand(redeployCmd)
}
