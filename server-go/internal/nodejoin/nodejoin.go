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
	"encoding/base64"
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

// dial opens an SSH client honoring ctx for cancellation.
//
// Host-key handling: trust-on-first-use (TOFU) by default. The first
// time we connect to a host we record its public key in
// $KUSO_NODEJOIN_KNOWN_HOSTS (default ~/.ssh/kuso_known_hosts inside
// the server pod). Subsequent dials to the same host MUST present the
// same key or the dial fails — defending against MITM after the first
// successful join.
//
// To disable TOFU and accept any key (the previous behaviour), set
// KUSO_NODEJOIN_INSECURE_HOSTKEY=1. Use this only on a fully trusted
// LAN where you can't survive a one-time bootstrap MITM check.
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
	hostKeyCallback, err := buildHostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("ssh: host-key callback: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:            c.User,
		Auth:            []ssh.AuthMethod{},
		HostKeyCallback: hostKeyCallback,
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

// buildHostKeyCallback returns an ssh.HostKeyCallback that implements
// trust-on-first-use:
//
//   - On the first connection to a host, the public key fingerprint is
//     written to the known-hosts file (default ~/.ssh/kuso_known_hosts;
//     overridden via KUSO_NODEJOIN_KNOWN_HOSTS).
//   - On subsequent connections to the same host, the presented key
//     MUST match the recorded one or the dial fails.
//   - Setting KUSO_NODEJOIN_INSECURE_HOSTKEY=1 reverts to the legacy
//     accept-anything behaviour. Do this only on networks where you
//     can rule out a one-time MITM during initial join.
//
// The file format is one record per line: "host base64(key.Marshal())".
// Simple enough not to need a parser library, and tamper-evident enough
// that a misbehaving operator can re-inspect it manually.
func buildHostKeyCallback() (ssh.HostKeyCallback, error) {
	if os.Getenv("KUSO_NODEJOIN_INSECURE_HOSTKEY") == "1" {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	path := os.Getenv("KUSO_NODEJOIN_KNOWN_HOSTS")
	if path == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "/root"
		}
		path = home + "/.ssh/kuso_known_hosts"
	}
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		expected, err := readKnownHost(path, hostname)
		if err != nil {
			return fmt.Errorf("read known_hosts %s: %w", path, err)
		}
		presented := key.Marshal()
		if expected == nil {
			// First time we've seen this host. Pin the key.
			if err := appendKnownHost(path, hostname, presented); err != nil {
				return fmt.Errorf("pin host key for %s: %w", hostname, err)
			}
			return nil
		}
		if !bytes.Equal(expected, presented) {
			return fmt.Errorf("host key for %s does not match pinned key — possible MITM. Remove the entry from %s if you intended to rotate", hostname, path)
		}
		return nil
	}, nil
}

// readKnownHost reads the marshalled public key for hostname from the
// known-hosts file. Returns (nil, nil) when the file or host is missing.
func readKnownHost(path, hostname string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[0] != hostname {
			continue
		}
		raw, err := decodeBase64(fields[1])
		if err != nil {
			continue
		}
		return raw, nil
	}
	return nil, nil
}

// appendKnownHost atomically appends a host/key pair to the known-hosts
// file. Creates the directory + file (mode 0600 for the file, 0700 for
// the parent dir) if missing.
func appendKnownHost(path, hostname string, key []byte) error {
	dir := path
	if i := strings.LastIndex(dir, "/"); i > 0 {
		dir = dir[:i]
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s\n", hostname, encodeBase64(key))
	return err
}

func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
