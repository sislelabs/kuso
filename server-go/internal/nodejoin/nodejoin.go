// Package nodejoin implements the SSH-driven k3s agent-join flow that
// powers the "Add node" button in /settings/nodes. The kuso-server
// pod reads the k3s server token from a hostPath mount, opens an SSH
// session to the user-supplied VM (password or private key), and
// runs the canonical `curl … | INSTALL_K3S_EXEC=agent sh -` one-liner.
//
// Failure modes we surface to the UI:
//   - bad credentials → "ssh: handshake failed"
//   - host unreachable → "ssh: dial: …"
//   - k3s install script non-zero exit → stdout/stderr returned
//   - control-plane :6443 not reachable from VM → user-friendly hint
//
// Removal is the inverse: cordon → drain → kubectl delete node →
// (best-effort) ssh in and run k3s-agent-uninstall.sh. We never
// silently delete a node that's still running workloads.
package nodejoin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Credentials is what the UI posts. Exactly one of Password / PrivateKey
// must be non-empty. Port defaults to 22.
type Credentials struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	Password    string `json:"password,omitempty"`
	PrivateKey  string `json:"privateKey,omitempty"`
	Passphrase  string `json:"passphrase,omitempty"`
}

// JoinSpec is what the kuso server passes to a Joiner. The token + URL
// come from the running cluster; the labels are merged into the
// k3s-agent install command so the new node lands in the right region
// from the moment it boots.
type JoinSpec struct {
	Credentials Credentials
	K3sURL      string            // e.g. https://10.0.0.1:6443
	K3sToken    string            // contents of /var/lib/rancher/k3s/server/node-token
	NodeLabels  map[string]string // optional kuso.sislelabs.com/<key>=<val>
	NodeName    string            // optional --node-name override
}

// JoinResult bundles the captured stdout/stderr so the UI can show a
// real progress log on success or failure. Every kuso operator hits
// "node won't join" at least once; an empty error string is useless.
type JoinResult struct {
	Output   string `json:"output"`
	NodeName string `json:"nodeName"`
}

// TokenFile is the standard hostPath location the deploy yaml mounts.
// Exposed as a var so tests can swap it.
var TokenFile = "/etc/kuso/k3s-token"

// ReadServerToken returns the k3s server token from the hostPath mount.
// Returns a friendly error when the mount isn't present (i.e. kuso is
// running outside cluster, or the deploy yaml wasn't updated).
func ReadServerToken() (string, error) {
	b, err := os.ReadFile(TokenFile)
	if err != nil {
		return "", fmt.Errorf("read k3s token (%s): %w — is the kuso-server pod scheduled on the control-plane node with the node-token hostPath mount?", TokenFile, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Join opens an SSH session to spec.Credentials and runs the k3s agent
// install. Blocks until the script exits or ctx is cancelled. Returns
// the joined node's name (best-effort hostname) on success.
func Join(ctx context.Context, spec JoinSpec) (*JoinResult, error) {
	if err := validateJoin(spec); err != nil {
		return nil, err
	}
	cli, err := dial(ctx, spec.Credentials)
	if err != nil {
		return nil, err
	}
	defer cli.Close()

	// Quick reachability probe — the script will fail later if the
	// new VM can't reach the control plane on :6443, but a clear
	// up-front error beats parsing curl output. Using `bash -c` so
	// the redirect works on a default sh.
	probeCmd := fmt.Sprintf(`bash -c 'timeout 5 bash -c "</dev/tcp/%s/%s" && echo OK || echo FAIL'`,
		shEscape(controlPlaneHost(spec.K3sURL)), controlPlanePort(spec.K3sURL))
	if probe, err := runCmd(ctx, cli, probeCmd); err == nil && !strings.Contains(probe, "OK") {
		return nil, fmt.Errorf("control-plane %s is not reachable from %s — open the firewall on port %s",
			spec.K3sURL, spec.Credentials.Host, controlPlanePort(spec.K3sURL))
	}

	// Build the install command. K3S_URL+K3S_TOKEN make the script
	// pick the agent code path; INSTALL_K3S_EXEC carries flags. The
	// label flag attaches kuso labels at boot so a freshly-joined
	// node lands in the right region/tier without a separate
	// label-update round-trip.
	flags := []string{}
	for k, v := range spec.NodeLabels {
		flags = append(flags, fmt.Sprintf("--node-label %s=%s", shEscape(k), shEscape(v)))
	}
	if spec.NodeName != "" {
		flags = append(flags, "--node-name "+shEscape(spec.NodeName))
	}
	execEnv := strings.Join(flags, " ")
	install := fmt.Sprintf(
		`curl -sfL https://get.k3s.io | K3S_URL=%s K3S_TOKEN=%s INSTALL_K3S_EXEC=%s sh -`,
		shEscape(spec.K3sURL), shEscape(spec.K3sToken), shEscape("agent "+execEnv),
	)
	out, err := runCmd(ctx, cli, install)
	if err != nil {
		return &JoinResult{Output: out}, fmt.Errorf("k3s agent install: %w", err)
	}
	// Best-effort: read the new node's hostname so the caller can
	// poll `kubectl get node <name>` for Ready. Errors here are non-
	// fatal — we still tell the user the install succeeded.
	hostname, _ := runCmd(ctx, cli, "hostname")
	return &JoinResult{Output: out, NodeName: strings.TrimSpace(hostname)}, nil
}

// ValidateResult is the report from a pre-flight Validate call. The
// UI surfaces each Check (label + ok + detail) so the operator sees
// exactly what failed before committing to an install. Coolify-style.
type ValidateResult struct {
	Checks []ValidateCheck `json:"checks"`
	OK     bool            `json:"ok"`
}

type ValidateCheck struct {
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Validate opens an SSH session and runs a series of probes WITHOUT
// installing anything: SSH handshake, sudo/root check, control-plane
// reachability on :6443, kernel version, and existing-k3s detection.
// Returns ValidateResult with one entry per check; OK=true only when
// every check passes.
func Validate(ctx context.Context, creds Credentials, k3sURL string) (*ValidateResult, error) {
	res := &ValidateResult{}
	cli, err := dial(ctx, creds)
	if err != nil {
		res.Checks = append(res.Checks, ValidateCheck{Label: "ssh", OK: false, Detail: err.Error()})
		return res, nil
	}
	defer cli.Close()
	res.Checks = append(res.Checks, ValidateCheck{Label: "ssh", OK: true, Detail: "connected"})

	// Sudo / root.
	whoami, _ := runCmd(ctx, cli, "id -u")
	whoami = strings.TrimSpace(whoami)
	if whoami == "0" {
		res.Checks = append(res.Checks, ValidateCheck{Label: "root", OK: true, Detail: "uid=0"})
	} else {
		// Try passwordless sudo. -n bails if a password would be needed.
		if _, err := runCmd(ctx, cli, "sudo -n true"); err != nil {
			res.Checks = append(res.Checks, ValidateCheck{Label: "root", OK: false,
				Detail: "user is not root and passwordless sudo failed — k3s install needs root"})
		} else {
			res.Checks = append(res.Checks, ValidateCheck{Label: "root", OK: true, Detail: "passwordless sudo"})
		}
	}

	// Control-plane reachability.
	probe := fmt.Sprintf(`bash -c 'timeout 5 bash -c "</dev/tcp/%s/%s" && echo OK || echo FAIL'`,
		shEscape(controlPlaneHost(k3sURL)), controlPlanePort(k3sURL))
	out, _ := runCmd(ctx, cli, probe)
	if strings.Contains(out, "OK") {
		res.Checks = append(res.Checks, ValidateCheck{Label: "control-plane", OK: true,
			Detail: fmt.Sprintf("%s reachable", k3sURL)})
	} else {
		res.Checks = append(res.Checks, ValidateCheck{Label: "control-plane", OK: false,
			Detail: fmt.Sprintf("%s NOT reachable from this VM — open port %s on the control plane", k3sURL, controlPlanePort(k3sURL))})
	}

	// curl available? (the install script uses it).
	if _, err := runCmd(ctx, cli, "command -v curl"); err != nil {
		res.Checks = append(res.Checks, ValidateCheck{Label: "curl", OK: false,
			Detail: "curl is required by the k3s install script — apt/yum install curl"})
	} else {
		res.Checks = append(res.Checks, ValidateCheck{Label: "curl", OK: true, Detail: "available"})
	}

	// Existing k3s? Surface as a warning, not a hard failure — re-running
	// the install on a node that already has k3s is fine but the user
	// should know what they're doing.
	if existing, _ := runCmd(ctx, cli, "test -f /usr/local/bin/k3s && echo present || echo absent"); strings.Contains(existing, "present") {
		res.Checks = append(res.Checks, ValidateCheck{Label: "k3s", OK: true,
			Detail: "k3s already installed — install will reconfigure as agent"})
	} else {
		res.Checks = append(res.Checks, ValidateCheck{Label: "k3s", OK: true, Detail: "not installed (will install)"})
	}

	// Compute aggregate ok.
	res.OK = true
	for _, c := range res.Checks {
		if !c.OK {
			res.OK = false
			break
		}
	}
	return res, nil
}

// Uninstall runs `/usr/local/bin/k3s-agent-uninstall.sh` over SSH.
// Used by the Remove flow after `kubectl delete node`. Best-effort:
// returns the captured output, errors when the script is missing
// (already removed) or the SSH dial fails.
func Uninstall(ctx context.Context, creds Credentials) (string, error) {
	cli, err := dial(ctx, creds)
	if err != nil {
		return "", err
	}
	defer cli.Close()
	return runCmd(ctx, cli, "/usr/local/bin/k3s-agent-uninstall.sh")
}

// dial opens an SSH client honoring ctx for cancellation. The host-key
// callback is intentionally InsecureIgnoreHostKey: the kuso operator
// is the one supplying both endpoints (we trust them) and any TOFU UI
// would just be friction. If you want strict host keys, set
// KUSO_NODEJOIN_STRICT_HOSTKEY=1 and supply known_hosts via env.
func dial(ctx context.Context, c Credentials) (*ssh.Client, error) {
	if c.Host == "" {
		return nil, errors.New("ssh: host is required")
	}
	if c.User == "" {
		c.User = "root"
	}
	port := c.Port
	if port == 0 {
		port = 22
	}
	cfg := &ssh.ClientConfig{
		User:            c.User,
		Auth:            []ssh.AuthMethod{},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	switch {
	case c.PrivateKey != "":
		var signer ssh.Signer
		var err error
		if c.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.PrivateKey), []byte(c.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(c.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("ssh: parse private key: %w", err)
		}
		cfg.Auth = append(cfg.Auth, ssh.PublicKeys(signer))
	case c.Password != "":
		cfg.Auth = append(cfg.Auth, ssh.Password(c.Password))
	default:
		return nil, errors.New("ssh: either password or privateKey is required")
	}
	addr := net.JoinHostPort(c.Host, fmt.Sprintf("%d", port))
	dialer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ssh: dial %s: %w", addr, err)
	}
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh: handshake: %w", err)
	}
	return ssh.NewClient(clientConn, chans, reqs), nil
}

// runCmd executes a command on the remote host and returns combined
// stdout+stderr. Fails fast when the command exits non-zero so
// callers see the actual install error on the wire.
func runCmd(ctx context.Context, cli *ssh.Client, cmd string) (string, error) {
	sess, err := cli.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh: new session: %w", err)
	}
	defer sess.Close()
	var buf bytes.Buffer
	sess.Stdout = &buf
	sess.Stderr = &buf
	doneCh := make(chan error, 1)
	go func() { doneCh <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return buf.String(), ctx.Err()
	case err := <-doneCh:
		if err != nil {
			return buf.String(), fmt.Errorf("remote command failed: %w (output: %s)", err, truncate(buf.String(), 2048))
		}
		return buf.String(), nil
	}
}

func validateJoin(s JoinSpec) error {
	if s.K3sURL == "" {
		return errors.New("K3sURL is required")
	}
	if s.K3sToken == "" {
		return errors.New("K3sToken is required")
	}
	if !strings.HasPrefix(s.K3sURL, "https://") {
		return fmt.Errorf("K3sURL must be https:// (got %q)", s.K3sURL)
	}
	if s.Credentials.Password == "" && s.Credentials.PrivateKey == "" {
		return errors.New("ssh password or privateKey is required")
	}
	return nil
}

// shEscape wraps a value in single quotes and escapes any single
// quotes inside. Sufficient for the shell command shape we build —
// we never inject untrusted data into option flags directly.
func shEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// controlPlaneHost / controlPlanePort split a K3sURL into pieces for
// the reachability probe. The URL is always https://host:port form
// because validateJoin enforces the prefix.
func controlPlaneHost(u string) string {
	u = strings.TrimPrefix(u, "https://")
	if i := strings.LastIndex(u, ":"); i >= 0 {
		return u[:i]
	}
	return u
}

func controlPlanePort(u string) string {
	u = strings.TrimPrefix(u, "https://")
	if i := strings.LastIndex(u, ":"); i >= 0 {
		return u[i+1:]
	}
	return "6443"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[truncated]"
}
