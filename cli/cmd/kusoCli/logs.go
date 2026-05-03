package kusoCli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

// `kuso logs <project> <service>` — print recent log lines from the
// pods backing a service's environment. One-shot tail; no streaming
// yet (a websocket-based --follow lands later).
//
//   kuso logs hello web
//   kuso logs hello web --env preview-pr-42 --lines 500

var (
	logsEnv    string
	logsLines  int
	logsFollow bool
)

var logsCmd = &cobra.Command{
	Use:   "logs <project> <service>",
	Short: "Print recent log lines from a service's pods",
	Long: `Print recent log lines from a service's pods.

Without --follow, prints the last N lines and exits (the legacy
behaviour). With --follow / -f, opens a WebSocket and streams new
log lines until ^C — same surface as the web UI's Logs tab.`,
	Example: `  kuso logs hello web
  kuso logs hello web -f --env staging
  kuso logs hello web --lines 1000`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if logsFollow {
			return streamLogs(args[0], args[1], logsEnv, logsLines)
		}
		// Non-follow path: hit the REST endpoint, dump, exit.
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

// streamLogs opens the same WebSocket the web UI uses and prints
// each frame as it arrives. Exits cleanly on ^C; reconnects are
// out of scope (CLI sessions are short by definition — if the
// connection drops, the operator can re-run the command).
func streamLogs(project, service, env string, tail int) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	base := api.BaseURL()
	if base == "" {
		return fmt.Errorf("no API URL configured")
	}
	tok := api.BearerToken()
	if tok == "" {
		return fmt.Errorf("no bearer token; run 'kuso login' first")
	}

	// http(s):// -> ws(s)://. Reuse the URL parser so a custom port
	// or path prefix on baseURL survives.
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("parse base url: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") +
		fmt.Sprintf("/ws/projects/%s/services/%s/logs",
			url.PathEscape(project), url.PathEscape(service))
	q := u.Query()
	q.Set("env", env)
	q.Set("tail", fmt.Sprintf("%d", tail))
	u.RawQuery = q.Encode()

	// Server expects "kuso.bearer, <jwt>" as a comma-separated
	// subprotocol list — the JWT slot needs to be the next entry
	// after the literal kuso.bearer name. Browsers split the
	// list themselves; here we hand it to gorilla as []string.
	dialer := websocket.DefaultDialer
	dialer.Subprotocols = []string{"kuso.bearer", tok}
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("ws connect: %w", err)
	}
	defer conn.Close()

	// ^C unwinds cleanly. Without this the CLI hangs on the
	// blocking ReadJSON loop and the user has to send SIGKILL.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		_ = conn.Close()
	}()

	for {
		var f struct {
			Type   string `json:"type"`
			Pod    string `json:"pod,omitempty"`
			Line   string `json:"line,omitempty"`
			Stream string `json:"stream,omitempty"`
			Value  string `json:"value,omitempty"`
		}
		if err := conn.ReadJSON(&f); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			return nil // signal-induced close looks the same; just exit quietly
		}
		switch f.Type {
		case "log":
			fmt.Printf("[%s] %s\n", f.Pod, f.Line)
		case "phase":
			fmt.Fprintf(os.Stderr, "==> phase: %s\n", f.Value)
		case "error":
			fmt.Fprintf(os.Stderr, "==> error: %s\n", f.Line)
		}
	}
}

func init() {
	rootCmd.AddCommand(logsCmd)
	logsCmd.Flags().StringVar(&logsEnv, "env", "production", "environment (production|preview-pr-N|<custom>)")
	logsCmd.Flags().IntVar(&logsLines, "lines", 200, "number of lines to fetch (max 2000)")
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "stream live logs over WebSocket until ^C")
}
