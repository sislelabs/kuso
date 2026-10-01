package kusoCli

import (
	"crypto/tls"
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

// wsDialer returns a WebSocket dialer configured for kuso, with the
// given subprotocols set. It CLONES websocket.DefaultDialer rather than
// mutating the package-global (two concurrent dials — e.g. `db pf` with
// several psql tabs — otherwise stomp each other's Subprotocols/TLS).
//
// It honours KUSO_INSECURE=1 the same way the REST client does
// (kusoApi.main.go): on a fresh install the instance serves Let's
// Encrypt *staging* certs that Go's TLS stack rejects, so REST worked
// (it read the env var) but every WebSocket dial — logs --follow, db
// port-forward — failed x509. Clone + set InsecureSkipVerify under the
// same env gate so both surfaces agree.
func wsDialer(subprotocols []string) *websocket.Dialer {
	d := *websocket.DefaultDialer // copy the struct, don't alias the global
	d.Subprotocols = subprotocols
	if v := strings.TrimSpace(os.Getenv("KUSO_INSECURE")); v == "1" || strings.EqualFold(v, "true") {
		tc := &tls.Config{InsecureSkipVerify: true}
		if d.TLSClientConfig != nil {
			tc = d.TLSClientConfig.Clone()
			tc.InsecureSkipVerify = true
		}
		d.TLSClientConfig = tc
	}
	return &d
}

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
	logsBuild  string
)

var logsCmd = &cobra.Command{
	Use:   "logs <project> <service>",
	Short: "Print recent log lines from a service's pods",
	Long: `Print recent log lines from a service's pods.

Without --follow, prints the newest --lines N lines and exits. With
several replicas the server tails N lines from each pod, merges them by
timestamp, and keeps the newest N, so the output is one ordered stream. With --follow / -f, opens a WebSocket and streams new
log lines until ^C — same surface as the web UI's Logs tab.`,
	Example: `  kuso logs hello web
  kuso logs hello web -f --env staging
  kuso logs hello web --lines 1000
  kuso logs hello web --build hello-web-main-mox0g7ry`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// --build <id> is a friendly alias for --env build:<id>.
		// Without this, users had to remember the magic prefix and
		// retype the build name; with it, they can copy the id
		// straight from `kuso build list` output. Mutually
		// exclusive with --env so a typo doesn't silently win.
		envSelector := logsEnv
		if logsBuild != "" {
			if logsEnv != "" && logsEnv != "production" {
				return fmt.Errorf("--build and --env are mutually exclusive")
			}
			envSelector = "build:" + logsBuild
		}
		if logsLines < 1 || logsLines > 2000 {
			return fmt.Errorf("--lines must be between 1 and 2000 (got %d)", logsLines)
		}
		// A stopped or scaled-to-zero env has no pods: the non-follow
		// path printed nothing and -f hung silently. Say so instead.
		var podCount = -1
		if logsBuild == "" {
			podCount = countServicePods(args[0], args[1], envSelector)
		}
		if logsFollow {
			if podCount == 0 {
				return fmt.Errorf("no pods running for %s/%s in env %s (stopped, sleeping or scaled to zero) — nothing to follow", args[0], args[1], envSelector)
			}
			return streamLogs(args[0], args[1], envSelector, logsLines)
		}
		// Non-follow path: hit the REST endpoint, dump, exit.
		path := fmt.Sprintf("/api/projects/%s/services/%s/logs?env=%s&lines=%d",
			args[0], args[1], url.QueryEscape(envSelector), logsLines)
		resp, err := api.RawGet(path)
		if err := checkRespErr(resp, err); err != nil {
			return err
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
		if len(data.Lines) == 0 && podCount == 0 {
			fmt.Fprintf(os.Stderr, "no pods running for %s/%s in env %s (stopped, sleeping or scaled to zero); `kuso logs search` reads the persisted archive\n", args[0], args[1], envSelector)
		}
		return nil
	},
}

// countServicePods returns the number of pods backing the env, or -1
// when the lookup fails (the caller then behaves as before).
func countServicePods(project, service, env string) int {
	resp, err := api.RawGet(fmt.Sprintf("/api/projects/%s/services/%s/pods?env=%s",
		url.PathEscape(project), url.PathEscape(service), url.QueryEscape(env)))
	if err != nil || resp.StatusCode() >= 300 {
		return -1
	}
	var info struct {
		Pods []json.RawMessage `json:"pods"`
	}
	if json.Unmarshal(resp.Body(), &info) != nil {
		return -1
	}
	return len(info.Pods)
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
	dialer := wsDialer([]string{"kuso.bearer", tok})
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("ws connect: %w", err)
	}
	defer conn.Close()

	// ^C unwinds cleanly. Without this the CLI hangs on the
	// blocking ReadJSON loop and the user has to send SIGKILL.
	// signaled tracks whether the close came from a signal handler
	// vs. a remote/transport failure — only the former should exit 0.
	// The previous code returned nil on every ReadJSON error, so a
	// CI script piping `kuso logs -f service > log.txt` couldn't tell
	// a clean unsubscribe from a network blip and had to scrape the
	// output to detect failure.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	signaled := make(chan struct{})
	go func() {
		<-sigCh
		close(signaled)
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
			select {
			case <-signaled:
				// User-initiated stop. Exit clean.
				return nil
			default:
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			// Surface every other failure as a non-zero exit so CI
			// scripts can detect a broken stream.
			return fmt.Errorf("ws read: %w", err)
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
	logsCmd.Flags().IntVar(&logsLines, "lines", 200, "number of lines to show, 1-2000: the newest N across all the env's pods (each pod's last N, merged by timestamp)")
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "stream live logs over WebSocket until ^C")
	logsCmd.Flags().StringVar(&logsBuild, "build", "", "tail this `build-id`'s pod logs (ids from kuso build list)")
}
