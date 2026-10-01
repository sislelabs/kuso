package kusoCli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// `kuso shell <project> <service>` — opens an interactive shell in one
// of the pods backing a service, through the server's terminal
// WebSocket (the same one the web UI's terminal uses). That keeps kuso
// auth, RBAC and audit in the path and needs no cluster credentials.
//
// --kubectl keeps the old local-kubectl path for a custom --command.
// It used to run against whatever kubeconfig context was current, which
// on a machine with several clusters could exec into the wrong one, so
// it now requires an explicit --context.

var (
	shellEnv       string
	shellContainer string
	shellCmd       string
	shellPod       string
	shellKubectl   bool
	shellContext   string
)

var shellCmdCobra = &cobra.Command{
	Use:   "shell <project> <service>",
	Short: "Open a shell in a service's pod",
	Long: `Open an interactive shell (sh -l) in one of the pods backing a service.

The session goes through the kuso server's terminal WebSocket, the same
one the web UI uses: it needs the admin role on the project, is
audit-logged, and needs no kubeconfig.

--kubectl execs with your local kubectl instead (for a custom --command).
It requires --context naming the kubeconfig context of the cluster this
kuso server runs on, so it can't land in another cluster by accident.`,
	Example: `  kuso shell hello web
  kuso shell hello web --env staging
  kuso shell hello web --kubectl --context kuso-prod --command /bin/bash`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if shellKubectl {
			return runKubectlShell(args[0], args[1])
		}
		if cmd.Flags().Changed("command") {
			return fmt.Errorf("--command needs --kubectl (the server terminal always runs sh -l)")
		}
		return runWSShell(args[0], args[1])
	},
}

func shellTerminalURL(base, project, service, env, pod, container string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported API URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") +
		fmt.Sprintf("/ws/projects/%s/services/%s/terminal", url.PathEscape(project), url.PathEscape(service))
	q := url.Values{}
	if env != "" {
		q.Set("env", env)
	}
	if pod != "" {
		q.Set("pod", pod)
	}
	if container != "" {
		q.Set("container", container)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func runWSShell(project, service string) error {
	base := api.BaseURL()
	if base == "" {
		return fmt.Errorf("no API URL configured")
	}
	tok := api.BearerToken()
	if tok == "" {
		return fmt.Errorf("no bearer token; run 'kuso login' first")
	}
	target, err := shellTerminalURL(base, project, service, shellEnv, shellPod, shellContainer)
	if err != nil {
		return err
	}
	conn, resp, err := wsDialer([]string{"kuso.bearer", tok}).Dial(target, nil)
	if err != nil {
		// The server answers pre-upgrade failures (403 no admin role,
		// 503 no running pods) with a JSON error body; show it.
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			msg := strings.TrimSpace(errEnvelopeMessage(string(body)))
			if msg == "" {
				msg = resp.Status
			}
			return fmt.Errorf("shell: %s", msg)
		}
		return fmt.Errorf("shell: ws connect: %w", err)
	}
	defer conn.Close()

	stdinFd := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFd) {
		old, err := term.MakeRaw(stdinFd)
		if err != nil {
			return fmt.Errorf("shell: raw mode: %w", err)
		}
		defer func() { _ = term.Restore(stdinFd, old) }()
	}

	// gorilla allows one concurrent writer; stdin and resize both write.
	var writeMu sync.Mutex
	send := func(mt int, b []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(mt, b)
	}
	sendSize := func() {
		cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || cols <= 0 || rows <= 0 {
			return
		}
		b, _ := json.Marshal(map[string]map[string]int{"resize": {"cols": cols, "rows": rows}})
		_ = send(websocket.TextMessage, b)
	}
	sendSize()
	stopResize := watchTerminalResize(sendSize)
	defer stopResize()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				// Binary frames: keystrokes needn't be valid UTF-8.
				if send(websocket.BinaryMessage, append([]byte(nil), buf[:n]...)) != nil {
					return
				}
			}
			if err != nil {
				_ = send(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) && (ce.Code == websocket.CloseNormalClosure || ce.Code == websocket.CloseGoingAway) {
				return nil
			}
			return fmt.Errorf("shell: %w", err)
		}
		if _, err := os.Stdout.Write(data); err != nil {
			return err
		}
	}
}

func runKubectlShell(project, service string) error {
	if shellContext == "" {
		return fmt.Errorf("--kubectl requires --context <kubeconfig-context> naming the cluster this kuso server runs on (your current context may be a different cluster)")
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl not on PATH")
	}
	// reason=shell asks the server to audit-log the lookup — the exec
	// itself runs locally, so this is the only server-side trace.
	path := fmt.Sprintf("/api/projects/%s/services/%s/pods?env=%s&reason=shell",
		url.PathEscape(project), url.PathEscape(service), url.QueryEscape(shellEnv))
	resp, err := api.RawGet(path)
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("pod lookup: %w", err)
	}
	var info struct {
		Namespace string `json:"namespace"`
		Pods      []struct {
			Name  string `json:"name"`
			Ready bool   `json:"ready"`
		} `json:"pods"`
	}
	if err := json.Unmarshal(resp.Body(), &info); err != nil {
		return fmt.Errorf("decode pods: %w", err)
	}
	if len(info.Pods) == 0 {
		return fmt.Errorf("no pods running for %s/%s in env %s", project, service, shellEnv)
	}
	target := info.Pods[0].Name
	for _, p := range info.Pods {
		if shellPod != "" && p.Name == shellPod {
			target = p.Name
			break
		}
		if shellPod == "" && p.Ready {
			target = p.Name
			break
		}
	}

	kArgs := []string{"--context", shellContext, "-n", info.Namespace, "exec", "-it", target}
	if shellContainer != "" {
		kArgs = append(kArgs, "-c", shellContainer)
	}
	kArgs = append(kArgs, "--", shellCmd)
	fmt.Fprintf(os.Stderr, "==> kubectl %v\n", kArgs)
	c := exec.Command("kubectl", kArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func init() {
	rootCmd.AddCommand(shellCmdCobra)
	shellCmdCobra.Flags().StringVar(&shellEnv, "env", "production", "environment (production|preview-pr-N|<custom>)")
	shellCmdCobra.Flags().StringVarP(&shellContainer, "container", "c", "", "container name (defaults to first)")
	shellCmdCobra.Flags().StringVar(&shellPod, "pod", "", "pod name (defaults to the first Ready pod)")
	shellCmdCobra.Flags().BoolVar(&shellKubectl, "kubectl", false, "exec with local kubectl instead of the server terminal (needs --context)")
	shellCmdCobra.Flags().StringVar(&shellContext, "context", "", "kubeconfig context for --kubectl")
	shellCmdCobra.Flags().StringVar(&shellCmd, "command", "/bin/sh", "command to exec inside the pod (--kubectl only)")
}
