package kusoCli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// logsSearchOutput backs `kuso logs search -o`. Deliberately NOT the
// shared outputFormat: its "text" default would become the default for
// every other -o command (pflag writes defaults at registration time
// into the bound variable, last init() wins).
var logsSearchOutput string

// `kuso logs search` — substring search over the persisted log
// archive. NOT full-text: FTS5 was removed server-side in v0.9 and
// SearchLogs now runs a plain case-insensitive ILIKE (db/log_db.go).
// There is no phrase quoting, no AND/OR/NOT, no prefix operator — the
// query is matched literally, so quoting a phrase searches for the
// quote characters themselves and reliably returns nothing.

var logsSearchCmd = &cobra.Command{
	Use:   "search <project> [service]",
	Short: "Search the persisted log archive (case-insensitive substring)",
	Args:  cobra.RangeArgs(1, 2),
	Example: `  kuso logs search myproj api --q OOMKilled --since 1h
  kuso logs search myproj --q 'connection refused' --limit 100
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
		if logsSearchOutput == "json" {
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
	if d, err := parseLookback(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	// The whole string must be digits; a Sscanf("%d") prefix scan read
	// "7d" as unix second 7.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		return time.Unix(n, 0), nil
	}
	return time.Time{}, fmt.Errorf("could not parse time %q (try 30m, 1h, 7d, 2026-01-01, 2026-01-01T00:00:00Z, or unix seconds)", s)
}

// parseLookback is time.ParseDuration plus the d and w units it lacks.
// Mirrors server-go's parseRangeDuration: only a bare "<n>d"/"<n>w" is
// special-cased, not compound forms like "1d12h".
func parseLookback(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return 0, fmt.Errorf("duration %q must be positive", s)
		}
		return d, nil
	}
	var unit time.Duration
	switch {
	case strings.HasSuffix(s, "d"):
		unit = 24 * time.Hour
	case strings.HasSuffix(s, "w"):
		unit = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	n, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return time.Duration(n * float64(unit)), nil
}

func init() {
	logsCmd.AddCommand(logsSearchCmd)
	logsSearchCmd.Flags().StringVar(&logsSearchQ, "q", "", "case-insensitive substring to find in log lines (matched literally — no quoting/AND/OR/prefix)")
	logsSearchCmd.Flags().StringVar(&logsSearchEnv, "env", "", "filter by env (production, preview-pr-N)")
	logsSearchCmd.Flags().StringVar(&logsSearchLimit, "limit", "100", "max lines to return (server caps at 500)")
	logsSearchCmd.Flags().StringVar(&logsSearchSince, "since", "", "lower bound (1h, 7d, 2026-01-01, RFC3339, unix)")
	logsSearchCmd.Flags().StringVar(&logsSearchUntil, "until", "", "upper bound (1h/7d ago, 2026-01-01, RFC3339, unix)")
	logsSearchCmd.Flags().StringVarP(&logsSearchOutput, "output", "o", "text", "output format [text, json]")
}
