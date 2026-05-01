package kusoCli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// `kuso logs <project> <service>` — print recent log lines from the
// pods backing a service's environment. One-shot tail; no streaming
// yet (a websocket-based --follow lands later).
//
//   kuso logs hello web
//   kuso logs hello web --env preview-pr-42 --lines 500

var (
	logsEnv   string
	logsLines int
)

var logsCmd = &cobra.Command{
	Use:   "logs <project> <service>",
	Short: "Print recent log lines from a service's pods",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		path := fmt.Sprintf("/api/projects/%s/services/%s/logs?env=%s&lines=%d",
			args[0], args[1], logsEnv, logsLines)
		resp, err := api.RawGet(path)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var data struct {
			Lines []struct {
				Pod  string `json:"pod"`
				Line string `json:"line"`
			} `json:"lines"`
		}
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		for _, l := range data.Lines {
			fmt.Printf("[%s] %s\n", l.Pod, l.Line)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
	logsCmd.Flags().StringVar(&logsEnv, "env", "production", "environment (production|preview-pr-N)")
	logsCmd.Flags().IntVar(&logsLines, "lines", 200, "number of lines to fetch (max 2000)")
}
