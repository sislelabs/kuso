package kusoCli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// `kuso logs search` — full-text search over the SQLite-stored
// log archive. Backed by FTS5 server-side; query grammar is
// FTS5 standard (phrase quoting, AND/OR/NOT, prefix `foo*`).

var logsSearchCmd = &cobra.Command{
	Use:   "search <project> [service]",
	Short: "Search the persisted log archive (FTS5 MATCH)",
	Args:  cobra.RangeArgs(1, 2),
	Example: `  kuso logs search myproj api --q OOMKilled --since 1h
  kuso logs search myproj --q '"connection refused"' --limit 100
  kuso logs search myproj api --since 2026-05-04T08:00:00Z --until 2026-05-04T09:00:00Z`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project := args[0]
		service := ""
		if len(args) == 2 {
			service = args[1]
		}
		params := map[string]string{
			"q":     logsSearchQ,
			"env":   logsSearchEnv,
			"limit": logsSearchLimit,
		}
		if logsSearchSince != "" {
			if t, err := parseSinceFlag(logsSearchSince); err == nil {
				params["since"] = t.UTC().Format(time.RFC3339)
			} else {
				return err
			}
		}
		if logsSearchUntil != "" {
			if t, err := parseSinceFlag(logsSearchUntil); err == nil {
				params["until"] = t.UTC().Format(time.RFC3339)
			} else {
				return err
			}
		}
		var code int
		var body []byte
		var err error
		if service == "" {
			r, e := api.SearchProjectLogs(project, params)
			err = e
			if r != nil {
				code, body = r.StatusCode(), r.Body()
			}
		} else {
			r, e := api.SearchLogs(project, service, params)
			err = e
			if r != nil {
				code, body = r.StatusCode(), r.Body()
			}
		}
		if err != nil {
			return err
		}
		if code >= 300 {
			return fmt.Errorf("server returned %d: %s", code, string(body))
		}
		if outputFormat == "json" {
			fmt.Println(string(body))
			return nil
		}
		var out struct {
			Lines []struct {
				Ts      string `json:"ts"`
				Pod     string `json:"pod"`
				Project string `json:"project"`
				Service string `json:"service"`
				Env     string `json:"env"`
				Line    string `json:"line"`
			} `json:"lines"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		// Print oldest-first so a tail-follow user sees chronological
		// output. Server returns newest-first; reverse here.
		for i := len(out.Lines) - 1; i >= 0; i-- {
			l := out.Lines[i]
			fmt.Printf("%s [%s/%s/%s/%s] %s\n",
				l.Ts, l.Project, l.Service, l.Env, l.Pod, strings.TrimRight(l.Line, "\n"))
		}
		return nil
	},
}

var (
	logsSearchQ     string
	logsSearchEnv   string
	logsSearchLimit string
	logsSearchSince string
	logsSearchUntil string
)

func parseSinceFlag(s string) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
		return time.Unix(n, 0), nil
	}
	return time.Time{}, fmt.Errorf("could not parse time %q (try 1h, 2026-01-01T00:00:00Z, or unix seconds)", s)
}

func init() {
	logsCmd.AddCommand(logsSearchCmd)
	logsSearchCmd.Flags().StringVar(&logsSearchQ, "q", "", "FTS5 MATCH — phrase with quotes, AND/OR/NOT, prefix foo*")
	logsSearchCmd.Flags().StringVar(&logsSearchEnv, "env", "", "filter by env (production, preview-pr-N)")
	logsSearchCmd.Flags().StringVar(&logsSearchLimit, "limit", "100", "max lines to return (server caps at 500)")
	logsSearchCmd.Flags().StringVar(&logsSearchSince, "since", "", "lower bound (1h, RFC3339, unix)")
	logsSearchCmd.Flags().StringVar(&logsSearchUntil, "until", "", "upper bound (RFC3339, unix)")
	logsSearchCmd.Flags().StringVarP(&outputFormat, "output", "o", "text", "output format [text, json]")
}
