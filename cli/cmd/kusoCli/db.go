package kusoCli

// `kuso db port-forward` (alias `pf`) opens a local TCP listener and
// tunnels every accepted connection through the kuso server's
// /ws/projects/{p}/addons/{a}/portforward WebSocket to the addon's
// pod. So a developer can run psql / TablePlus / pg_dump against a
// cluster-internal database without it being publicly reachable.
//
// `kuso db connect` is a convenience wrapper: opens the tunnel on an
// auto-picked local port, fetches the addon's conn secret (admin
// only), and prints a localhost-rewritten DSN the user copies into
// their tool. With --exec it execs psql/redis-cli directly.
//
// Admin-only — the server enforces settings:admin on the WS handler;
// the CLI checks the conn-secret fetch (which requires secrets:read)
// for the connect shortcut.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Connect to an addon database from your local environment",
	Long: `Connect to a project addon (postgres, redis, mongo, ...) from your
laptop without exposing the database publicly. The kuso server proxies
a single TCP stream per opened connection.

Requires the settings:admin role.`,
}

var (
	pfLocalPort int
	pfBindAddr  string
)

var pfCmd = &cobra.Command{
	Use:     "port-forward <project> <addon>",
	Aliases: []string{"pf"},
	Short:   "Open a local TCP listener tunnelled to an addon pod",
	Long: `Opens a local TCP listener and forwards every accepted connection
through the kuso server to the addon's pod. The tunnel stays open until
Ctrl-C; multiple concurrent connections (e.g. psql tabs) are supported.

By default the listener picks a free local port. Use --port to pin it.`,
	Example: `  kuso db pf myproj db
  kuso db pf myproj db --port 5433
  psql "postgres://kuso:<password>@localhost:5433/myproj"`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPortForward(args[0], args[1], pfBindAddr, pfLocalPort, "")
	},
}

var (
	connectExec bool
)

var dbConnectCmd = &cobra.Command{
	Use:   "connect <project> <addon>",
	Short: "Open a tunnel and print (or exec) a localhost connection string",
	Long: `Opens a port-forward to the addon and prints a localhost-rewritten
DSN you can paste into psql / TablePlus / pg_dump / etc. With --exec
the matching client (psql for postgres, redis-cli for redis) is run
directly against the tunnel.

Requires settings:admin (the addon conn-secret fetch is admin-gated).`,
	Example: `  kuso db connect myproj db
  kuso db connect myproj db --exec`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConnect(args[0], args[1], connectExec)
	},
}

func init() {
	pfCmd.Flags().IntVar(&pfLocalPort, "port", 0, "local port to bind (0 = pick a free one)")
	pfCmd.Flags().StringVar(&pfBindAddr, "bind", "127.0.0.1", "local address to bind")
	dbConnectCmd.Flags().BoolVar(&connectExec, "exec", false, "exec the matching client (psql/redis-cli) against the tunnel")
	dbCmd.AddCommand(pfCmd)
	dbCmd.AddCommand(dbConnectCmd)
	rootCmd.AddCommand(dbCmd)
}

// runPortForward opens the local TCP listener and tunnels each
// accepted connection through a fresh WebSocket to the kuso server.
// onReady, when non-nil, is called once the listener is up with the
// chosen local port — used by `kuso db connect` to print the DSN.
func runPortForward(project, addon, bindAddr string, localPort int, _exec string) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	wsURL, err := buildPortForwardURL(project, addon)
	if err != nil {
		return err
	}
	tok := api.BearerToken()
	if tok == "" {
		return fmt.Errorf("no bearer token; run 'kuso login' first")
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bindAddr, localPort))
	if err != nil {
		return fmt.Errorf("local listen: %w", err)
	}
	defer listener.Close()
	chosen := listener.Addr().(*net.TCPAddr)
	fmt.Fprintf(os.Stderr, "[kuso] forwarding %s:%d → %s/%s (Ctrl-C to stop)\n", chosen.IP, chosen.Port, project, addon)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGINT/SIGTERM unwinds the accept loop. Closing the listener is
	// the clean signal here — Accept returns an error and we exit.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "[kuso] shutting down")
		cancel()
		_ = listener.Close()
	}()

	var wg sync.WaitGroup
	for {
		c, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break // clean shutdown
			}
			return fmt.Errorf("accept: %w", err)
		}
		wg.Add(1)
		go func(local net.Conn) {
			defer wg.Done()
			defer local.Close()
			if err := proxyOneConnection(ctx, local, wsURL, tok); err != nil {
				fmt.Fprintf(os.Stderr, "[kuso] tunnel: %v\n", err)
			}
		}(c)
	}
	wg.Wait()
	return nil
}

// buildPortForwardURL turns the API base URL into the WS URL for the
// addon port-forward route. Mirrors the http→ws scheme rewrite the
// logs CLI does.
func buildPortForwardURL(project, addon string) (string, error) {
	base := api.BaseURL()
	if base == "" {
		return "", fmt.Errorf("no API URL configured")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") +
		fmt.Sprintf("/ws/projects/%s/addons/%s/portforward",
			url.PathEscape(project), url.PathEscape(addon))
	return u.String(), nil
}

// proxyOneConnection opens a fresh WebSocket and bridges the local
// TCP conn to it for the lifetime of the local conn. Each accepted
// local connection gets its own WS — matches how kube port-forward
// multiplexes underneath and keeps the bridge simple.
func proxyOneConnection(ctx context.Context, local net.Conn, wsURL, tok string) error {
	dialer := websocket.DefaultDialer
	dialer.Subprotocols = []string{"kuso.bearer", tok}
	ws, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer ws.Close()

	done := make(chan struct{}, 2)

	// local → ws
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, err := local.Read(buf)
			if n > 0 {
				if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// ws → local
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if _, werr := local.Write(msg); werr != nil {
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
	case <-done:
	}
	return nil
}

// runConnect opens a tunnel on a free local port, fetches the addon
// conn-secret, and either prints the rewritten DSN or execs the
// matching client tool against it.
func runConnect(project, addon string, doExec bool) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}

	// Fetch the addon's plaintext conn secret. Admin-gated server-side
	// (secrets:read); the CLI surfaces the 403 verbatim if the caller
	// lacks the permission.
	resp, err := api.RawGet(fmt.Sprintf("/api/projects/%s/addons/%s/secret",
		url.PathEscape(project), url.PathEscape(addon)))
	if err != nil {
		return fmt.Errorf("fetch addon secret: %w", err)
	}
	if resp.StatusCode() >= 300 {
		return fmt.Errorf("addon secret: %d %s", resp.StatusCode(), string(resp.Body()))
	}
	secret, err := decodeAddonSecret(resp.Body())
	if err != nil {
		return fmt.Errorf("decode secret: %w", err)
	}

	// Pick a free local port up front so we can print a deterministic
	// DSN before the user runs the client.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("local listen: %w", err)
	}
	localPort := l.Addr().(*net.TCPAddr).Port
	l.Close()

	// Build the localhost-rewritten connection string from the
	// addon's secret. We rewrite the canonical DSN (DATABASE_URL /
	// REDIS_URL / MONGODB_URL / …) by replacing the host:port with
	// our chosen localhost:port.
	kindHint, dsn := localDSNFromSecret(secret, localPort)
	if dsn == "" {
		return fmt.Errorf("could not derive a connection string from the addon secret (kind %q)", kindHint)
	}

	if doExec {
		// Print the DSN to stderr (so a piped psql output stays clean
		// on stdout) and launch the matching client.
		fmt.Fprintf(os.Stderr, "[kuso] tunnel + %s → %s\n", clientForKind(kindHint), dsn)
	} else {
		fmt.Fprintf(os.Stderr, "[kuso] tunnel ready. Connect with:\n")
		fmt.Println(dsn)
	}

	// Open the tunnel in a goroutine — runPortForward blocks on the
	// listener. The user's client connects to localhost:localPort.
	tunnelDone := make(chan error, 1)
	go func() {
		tunnelDone <- runPortForward(project, addon, "127.0.0.1", localPort, "")
	}()

	if doExec {
		// Wait a moment for the listener to be up, then exec the
		// client. The simplest robust signal is a quick TCP probe.
		if err := waitListening("127.0.0.1", localPort); err != nil {
			return err
		}
		return runClientFor(kindHint, dsn)
	}
	return <-tunnelDone
}

// decodeAddonSecret parses the GET /addons/{addon}/secret response into a flat
// map of connection values. The server wraps them under a "values" key
// (handlers.AddonsHandler.Secret writes {"values": {...}}); we decode that
// shape, falling back to a flat top-level map for older servers. The flat
// fallback is what the original code assumed unconditionally — which broke once
// the server adopted the wrapper, since {"values": {...}} fails to unmarshal
// into map[string]string ("cannot unmarshal object into Go value of type
// string").
func decodeAddonSecret(body []byte) (map[string]string, error) {
	var wrapped struct {
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Values != nil {
		return wrapped.Values, nil
	}
	// Legacy flat shape: {"DATABASE_URL": "...", ...}.
	var flat map[string]string
	if err := json.Unmarshal(body, &flat); err != nil {
		return nil, err
	}
	return flat, nil
}

// localDSNFromSecret returns (kindHint, dsn) — a rewritten connection
// string that points at localhost:port instead of the in-cluster
// service. kindHint is a best-effort label used to pick a client tool.
func localDSNFromSecret(s map[string]string, localPort int) (string, string) {
	// DATABASE_URL is the postgres/mysql canonical key; REDIS_URL is
	// redis; MONGODB_URL is mongo. The conn secrets the kuso addon
	// charts emit always carry one of these.
	if v := s["DATABASE_URL"]; v != "" {
		return "postgres", rewriteDSNHost(v, "127.0.0.1", localPort)
	}
	if v := s["REDIS_URL"]; v != "" {
		return "redis", rewriteDSNHost(v, "127.0.0.1", localPort)
	}
	if v := s["MONGODB_URL"]; v != "" {
		return "mongo", rewriteDSNHost(v, "127.0.0.1", localPort)
	}
	return "", ""
}

// rewriteDSNHost replaces the host:port in a URL-style DSN with the
// localhost-rewritten pair. Leaves user:password / database / query
// string intact.
func rewriteDSNHost(dsn, host string, port int) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	u.Host = fmt.Sprintf("%s:%d", host, port)
	return u.String()
}

// clientForKind picks the local CLI tool to exec for a given kind.
func clientForKind(kind string) string {
	switch kind {
	case "postgres":
		return "psql"
	case "redis":
		return "redis-cli"
	case "mongo":
		return "mongosh"
	default:
		return ""
	}
}

// runClientFor execs the matching client tool against the rewritten
// DSN. Inherits stdio so the user gets an interactive session.
func runClientFor(kind, dsn string) error {
	tool := clientForKind(kind)
	if tool == "" {
		return fmt.Errorf("no built-in client for kind %q — use the DSN above with your own tool", kind)
	}
	if _, err := exec.LookPath(tool); err != nil {
		return fmt.Errorf("%s not on PATH — install it or run without --exec to copy the DSN", tool)
	}
	cmd := exec.Command(tool, dsn)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// waitListening dials the local port repeatedly until it accepts a
// connection — a short bounded wait for the listener to come up.
// 50 × 20ms = 1s — plenty for an in-process listener.
//
// `net.JoinHostPort` over `fmt.Sprintf("%s:%d", ...)` because the
// host can be an IPv6 literal ("::1", "fd00::1") and the printf form
// produces `::1:5432` which is a single ambiguous address, not host
// + port. JoinHostPort wraps IPv6 literals in brackets correctly.
func waitListening(host string, port int) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	for i := 0; i < 50; i++ {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("tunnel listener didn't come up in time")
}
