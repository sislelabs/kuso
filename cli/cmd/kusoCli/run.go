// `kuso run` — fire a one-shot task pod against a service's
// most-recent succeeded build image. Closes the kubectl-exec gap
// for migrations, seeds, one-off scripts.
//
//   kuso run <project> <service> -- <command…>
//   kuso run alpha web -- python manage.py migrate
//   kuso run alpha web --set-env FOO=bar -- ./scripts/seed.sh
//
// The trailing args (after --) are the argv for the run container.
// We avoid shell expansion on the CLI side; users who need a shell
// pass `sh -c "your command"` explicitly.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"kuso/pkg/kusoApi"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var (
	runTimeoutSeconds int
	runSetEnvFlags    []string
	runEnvFlags       []string // deprecated --env KEY=VALUE, kept for one release
	runFollow         bool
)

var runCmd = &cobra.Command{
	Use:   "run <project> <service> -- <command…>",
	Short: "Run a one-shot task in a service's image (migrations, seeds, scripts)",
	// We require at least project + service + one command arg. The
	// `--` separator is conventionally where command args start;
	// cobra stops parsing flags after it.
	Args: cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service := args[0], args[1]
		command := args[2:]
		envVars, err := parseRunEnvFlags(append(append([]string{}, runSetEnvFlags...), runEnvFlags...))
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "Runs use the production environment's image and secrets.")
		req := kusoApi.CreateRunRequest{
			Command:        command,
			Env:            envVars,
			TimeoutSeconds: runTimeoutSeconds,
		}
		resp, err := api.CreateRun(project, service, req)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("create run: %w", err)
		}
		// Server returns the full KusoRun CR; pluck name + phase.
		var data struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			fmt.Println(string(resp.Body()))
			return nil
		}
		runName := data.Metadata.Name
		phase := data.Metadata.Annotations["kuso.sislelabs.com/run-phase"]
		fmt.Fprintf(os.Stderr, "run %s created (phase=%s)\n  command: %s\n",
			runName, phase, strings.Join(command, " "))

		if !runFollow {
			return nil
		}
		return streamRunUntilDone(project, service, runName)
	},
}

// streamRunUntilDone polls the run's phase, streams its logs once
// the pod has produced any, and exits with the run's exit code (1
// for failed/cancelled, 0 for succeeded).
//
// The poll loop is the simplest correct shape — there's no
// server-side `runs/{run}/follow` WebSocket today and adding one
// is a bigger lift than this CLI-side polling. Cadence (1s while
// pending, 2s while running) keeps cost down on long migrations
// while staying snappy on quick ones.
func streamRunUntilDone(project, service, runName string) error {
	var cur runLogCursor
	fetch := func(since string, n int) ([]runLogLine, error) {
		return fetchRunPage(project, service, runName, since, n)
	}
	for {
		resp, err := api.GetRun(project, runName)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("get run: %w", err)
		}
		var data struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode run: %w", err)
		}
		phase := data.Metadata.Annotations["kuso.sislelabs.com/run-phase"]
		// Pull logs every iteration once the pod might exist (running or
		// terminal) and print the lines that are new since the last poll.
		if phase == "running" || phase == "succeeded" || phase == "failed" || phase == "cancelled" {
			fresh, gap, _ := cur.poll(fetch, phase != "running")
			if gap {
				fmt.Fprintf(os.Stderr, "--- log output skipped: more lines arrived than one fetch returns; read the full log with `kuso logs %s %s --env run:%s --lines 2000` ---\n", project, service, runName)
			}
			for _, ln := range fresh {
				fmt.Println(ln)
			}
		}
		switch phase {
		case "succeeded":
			return nil
		case "failed":
			msg := data.Metadata.Annotations["kuso.sislelabs.com/run-message"]
			if msg == "" {
				msg = "run failed"
			}
			return fmt.Errorf("%s", msg)
		case "cancelled":
			return fmt.Errorf("run cancelled")
		case "running":
			time.Sleep(2 * time.Second)
		default:
			time.Sleep(1 * time.Second)
		}
	}
}

// runLogFollower turns successive bounded log tails into the lines that
// are new since the previous tail. The logs endpoint has no cursor, so
// it aligns the longest suffix of the previous window with a prefix of
// the new one. Indexing by a running line count (the old approach)
// stopped printing for good once the output outgrew the window.
type runLogFollower struct {
	prev []string
}

// next returns the new lines in cur. gap is true when cur shares no
// overlap with the previous window, i.e. more output arrived between
// fetches than one window holds (cur is then returned whole).
func (f *runLogFollower) next(cur []string) (fresh []string, gap bool) {
	prev := f.prev
	f.prev = cur
	if len(prev) == 0 {
		return cur, false
	}
	for k := min(len(prev), len(cur)); k > 0; k-- {
		if slices.Equal(prev[len(prev)-k:], cur[:k]) {
			return cur[k:], false
		}
	}
	if len(cur) == 0 {
		return nil, false
	}
	return cur, true
}

// runLogLine is one line of GET …/logs?env=run:<name>. Servers that
// support sinceTime paging set TS.
type runLogLine struct {
	Line string `json:"line"`
	TS   string `json:"ts"`
}

const (
	runLogPage  = 1000
	runLogEpoch = "1970-01-01T00:00:00Z"
)

// runLogCursor pages a run's log forward with sinceTime: since is the
// timestamp of the last printed line and atSince how many printed lines
// carry exactly that timestamp (the server returns lines AT since again).
// Against a server without sinceTime (lines carry no ts) it falls back to
// diffing bounded tails with runLogFollower.
type runLogCursor struct {
	since   string
	atSince int
	legacy  bool
	follow  runLogFollower
}

// poll returns the lines new since the previous poll; gap reports output
// lost in legacy mode. fetch(since, n) GETs the run log; since == "" means
// no sinceTime param.
func (c *runLogCursor) poll(fetch func(since string, n int) ([]runLogLine, error), terminal bool) ([]string, bool, error) {
	if c.legacy {
		n := 500
		if terminal {
			n = 2000
		}
		page, err := fetch("", n)
		if err != nil {
			return nil, false, err
		}
		fresh, gap := c.follow.next(runLogTexts(page))
		return fresh, gap, nil
	}
	var out []string
	for {
		since := c.since
		if since == "" {
			since = runLogEpoch
		}
		page, err := fetch(since, runLogPage)
		if err != nil {
			return out, false, err
		}
		if slices.ContainsFunc(page, func(l runLogLine) bool { return l.TS == "" }) {
			c.legacy = true
			fresh, gap := c.follow.next(runLogTexts(page))
			return append(out, fresh...), gap, nil
		}
		skip := 0
		for skip < len(page) && skip < c.atSince && page[skip].TS == c.since {
			skip++
		}
		out = append(out, runLogTexts(page[skip:])...)
		if len(page) > 0 {
			last := page[len(page)-1].TS
			trail := 0
			for i := len(page) - 1; i >= 0 && page[i].TS == last; i-- {
				trail++
			}
			if last == c.since && trail == len(page) {
				c.atSince = max(c.atSince, trail)
			} else {
				c.since, c.atSince = last, trail
			}
		}
		// A short page is the end; a full page that added nothing means
		// more than a page of lines share one timestamp, so stop.
		if len(page) < runLogPage || skip == len(page) {
			return out, false, nil
		}
	}
}

func runLogTexts(in []runLogLine) []string {
	out := make([]string, len(in))
	for i, l := range in {
		out[i] = l.Line
	}
	return out
}

// fetchRunPage pulls run-pod log lines via the /logs endpoint with
// env=run:<runName>, from sinceTime onwards when since is set.
func fetchRunPage(project, service, runName, since string, lines int) ([]runLogLine, error) {
	path := fmt.Sprintf("/api/projects/%s/services/%s/logs?env=%s&lines=%d",
		url.PathEscape(project), url.PathEscape(service), url.QueryEscape("run:"+runName), lines)
	if since != "" {
		path += "&sinceTime=" + url.QueryEscape(since)
	}
	resp, err := api.RawGet(path)
	if err := checkRespErr(resp, err); err != nil {
		return nil, fmt.Errorf("get logs: %w", err)
	}
	var data struct {
		Lines []runLogLine `json:"lines"`
	}
	if err := json.Unmarshal(resp.Body(), &data); err != nil {
		return nil, err
	}
	return data.Lines, nil
}

// parseRunEnvFlags turns []string{"K=V", "X=Y"} into the typed
// EnvVar slice the API expects. Empty values are allowed (FOO= ).
func parseRunEnvFlags(flags []string) ([]kusoApi.RunEnvVar, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	out := make([]kusoApi.RunEnvVar, 0, len(flags))
	for _, f := range flags {
		i := strings.IndexByte(f, '=')
		if i <= 0 {
			return nil, fmt.Errorf("--set-env %q: expected KEY=VALUE", f)
		}
		out = append(out, kusoApi.RunEnvVar{Name: f[:i], Value: f[i+1:]})
	}
	return out, nil
}

// runCancelCmd cancels an in-flight one-shot run. The CancelRun client
// method existed but was never wired to a command — you could start a run
// but not stop it from the CLI.
var runCancelCmd = &cobra.Command{
	Use:   "cancel <project> <run>",
	Short: "Cancel an in-flight run",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.CancelRun(args[0], args[1])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("cancel run: %w", err)
		}
		fmt.Printf("run %s/%s cancelled\n", args[0], args[1])
		return nil
	},
}

var runListCmd = &cobra.Command{
	Use:     "list <project> <service>",
	Aliases: []string{"ls"},
	Short:   "List a service's runs, newest first",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListRuns(args[0], args[1])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list runs: %w", err)
		}
		if outputFormat == "json" {
			fmt.Println(string(resp.Body()))
			return nil
		}
		var runs []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Command []string `json:"command"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(resp.Body(), &runs); err != nil {
			return fmt.Errorf("decode runs: %w", err)
		}
		if len(runs) == 0 {
			fmt.Println("No runs.")
			return nil
		}
		t := tablewriter.NewWriter(os.Stdout)
		t.SetHeader([]string{"NAME", "PHASE", "STARTED", "COMMAND"})
		for _, r := range runs {
			a := r.Metadata.Annotations
			t.Append([]string{
				r.Metadata.Name,
				a["kuso.sislelabs.com/run-phase"],
				a["kuso.sislelabs.com/run-started-at"],
				strings.Join(r.Spec.Command, " "),
			})
		}
		t.Render()
		return nil
	},
}

var runDeleteYes bool

// runDeleteCmd removes a finished run's record + pod. DeleteRun client
// method existed but was unwired.
var runDeleteCmd = &cobra.Command{
	Use:     "delete <project> <run>",
	Aliases: []string{"rm"},
	Short:   "Delete a run (record + pod)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(runDeleteYes,
			fmt.Sprintf("Delete run %s/%s?", args[0], args[1])); err != nil {
			return err
		}
		resp, err := api.DeleteRun(args[0], args[1])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("delete run: %w", err)
		}
		fmt.Printf("run %s/%s deleted\n", args[0], args[1])
		return nil
	},
}

func init() {
	runCmd.Flags().IntVar(&runTimeoutSeconds, "timeout-seconds", 0, "max run duration in seconds (default 1800 / 30 min)")
	runCmd.Flags().StringArrayVarP(&runSetEnvFlags, "set-env", "e", nil, "extra env var as `KEY=VALUE`, repeatable")
	// Separate slice: two pflag string arrays sharing one pointer reset
	// each other on first Set, dropping values.
	runCmd.Flags().StringArrayVar(&runEnvFlags, "env", nil, "extra env var as `KEY=VALUE` (deprecated: use --set-env)")
	_ = runCmd.Flags().MarkDeprecated("env", "use --set-env (or -e); --env will name an environment in a later release")
	runCmd.Flags().BoolVarP(&runFollow, "follow", "f", false, "stream logs + block until the run completes; exit code matches the run's exit code")
	runCmd.AddCommand(runListCmd)
	runListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	runCmd.AddCommand(runCancelCmd)
	runCmd.AddCommand(runDeleteCmd)
	runDeleteCmd.Flags().BoolVarP(&runDeleteYes, "yes", "y", false, "skip the confirmation prompt")
	rootCmd.AddCommand(runCmd)
}
