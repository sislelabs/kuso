package handlers

// terminal_ws.go hosts the browser-terminal WebSocket: an interactive
// `kubectl exec`-equivalent shell into a service's pod, surfaced as an
// xterm in the web UI.
//
//	GET /ws/projects/{project}/services/{service}/terminal?env=X&pod=Y&container=Z
//
// The kuso server proxies the kube exec stream so the browser never
// needs cluster credentials — auth is the same JWT + project-tenancy
// gate the logs WS uses. Shell sessions are audit-logged.
//
// Wire protocol (text frames, client→server):
//   - plain text  → stdin bytes, forwarded to the pod
//   - {"resize":{"cols":N,"rows":M}}  → TTY resize
// Server→client frames are raw stdout/stderr bytes (binary).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

// TerminalWSHandler serves the interactive pod-exec WebSocket.
type TerminalWSHandler struct {
	Svc    *projects.Service // pod resolution (ListPods)
	Kube   *kube.Client      // SPDY exec executor
	Issuer *auth.Issuer
	DB     *db.DB
	Audit  *audit.Service
	Logger *slog.Logger

	// Per-user concurrency cap, same rationale as LogsWSHandler:
	// a tab-storm must not open unbounded exec streams against the
	// kube apiserver.
	mu     sync.Mutex
	active map[string]int
}

// maxTerminalsPerUser caps concurrent shell sessions per principal.
const maxTerminalsPerUser = 4

// Mount registers the terminal route on the public router (the WS
// handler rolls its own JWT + tenancy gate, like the logs WS).
func (h *TerminalWSHandler) Mount(r chi.Router) {
	r.Get("/ws/projects/{project}/services/{service}/terminal", h.Terminal)
}

// Terminal upgrades to a WebSocket and proxies an interactive exec
// into one pod of the service.
func (h *TerminalWSHandler) Terminal(w http.ResponseWriter, r *http.Request) {
	jwtTok := extractWSBearer(r)
	if jwtTok == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	claims, err := h.Issuer.Verify(jwtTok)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Revocation: Verify only checks signature + expiry. This handler is
	// on the public router (no auth middleware), so we must consult the
	// revocation hook ourselves — otherwise a logged-out / deactivated
	// admin's unexpired token still opens a shell. Fails closed.
	if h.Issuer.CheckRevoked(r.Context(), claims) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// The auth middleware (which we bypass) is the only production code
	// that stuffs claims into the request context. callerHasProjectPerm
	// reads them back via ClaimsFromContext, so inject them here — without
	// this the perm check sees nil claims and 403s every caller, admins
	// included (the browser terminal was wholly inoperative before this).
	ctx := auth.ContextWithClaims(r.Context(), claims)
	r = r.WithContext(ctx)

	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")

	// Tenancy gate. A browser shell into a pod can `printenv` every
	// secret value, so in role-system v2 it requires the ADMIN role —
	// the same boundary as reading env values and the SQL console. This
	// is satisfied by an instance admin OR a project-admin on THIS
	// project (granted via a ProjectGrant with override=admin), resolved
	// the same way as callerCanReadSecrets — so the three secret-bearing
	// surfaces stay consistent.
	if !callerHasProjectPerm(r.Context(), h.DB, project, auth.PermShellExec) {
		http.Error(w, "forbidden: shell access requires the admin role", http.StatusForbidden)
		return
	}

	if !h.acquireSlot(claims.UserID) {
		http.Error(w, "too many concurrent shell sessions", http.StatusTooManyRequests)
		return
	}
	defer h.releaseSlot(claims.UserID)

	q := r.URL.Query()
	env := q.Get("env")

	// Resolve the target pod. The client may pass ?pod= explicitly
	// (the UI lets the user pick when a service has several replicas);
	// otherwise we take the first Ready pod.
	rctx, rcancel := context.WithTimeout(r.Context(), 10*time.Second)
	podList, err := h.Svc.ListPods(rctx, project, service, env)
	rcancel()
	if err != nil || podList == nil || len(podList.Pods) == 0 {
		http.Error(w, "no running pods for this service", http.StatusServiceUnavailable)
		return
	}
	pod := pickPod(podList.Pods, q.Get("pod"))
	if pod == nil {
		http.Error(w, "requested pod not found or not ready", http.StatusServiceUnavailable)
		return
	}
	container := q.Get("container")
	if container == "" && len(pod.Containers) > 0 {
		container = pod.Containers[0]
	}

	// Upgrade. Echo the bearer subprotocol so intermediaries (Traefik)
	// settle the handshake — same dance as the logs WS.
	respHeader := http.Header{}
	if sp := r.Header.Get("Sec-WebSocket-Protocol"); sp != "" {
		respHeader.Set("Sec-WebSocket-Protocol", "kuso.bearer")
	}
	conn, err := upgrader.Upgrade(w, r, respHeader)
	if err != nil {
		return // upgrader wrote its own error
	}
	defer conn.Close()

	if h.Audit != nil {
		h.Audit.Log(r.Context(), audit.Entry{
			User:     claims.Username,
			Severity: "warn",
			Action:   "service.terminal",
			Pipeline: project,
			App:      service,
			Resource: "kuspod",
			Message:  "browser terminal opened on pod " + pod.Name + " (container " + container + ")",
		})
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// stdinPipe carries bytes from the WS read loop to the kube exec
	// stream. resizeCh carries TTY size changes. Both close when the
	// client disconnects so the exec stream unwinds.
	stdin := &wsStdin{conn: conn, resize: make(chan remotecommand.TerminalSize, 4)}
	conn.SetCloseHandler(func(code int, text string) error {
		cancel()
		stdin.closeOnce()
		return nil
	})

	exec, err := h.newExecutor(podList.Namespace, pod.Name, container)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("\r\n[kuso] exec setup failed: "+err.Error()+"\r\n"))
		return
	}

	out := &wsStdout{conn: conn}
	streamErr := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             stdin,
		Stdout:            out,
		Stderr:            out,
		Tty:               true,
		TerminalSizeQueue: stdin,
	})
	if streamErr != nil && ctx.Err() == nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("\r\n[kuso] session ended: "+streamErr.Error()+"\r\n"))
	}
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session closed"),
		time.Now().Add(2*time.Second),
	)
}

// newExecutor builds a SPDY exec executor for an interactive shell.
// It runs `sh` — the most portable shell; images without bash still
// have it. The exec requests a TTY + stdin so the pod sees a real
// interactive terminal.
func (h *TerminalWSHandler) newExecutor(ns, podName, container string) (remotecommand.Executor, error) {
	req := h.Kube.Clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(podName).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			// `sh -l` for a login shell so PATH/prompt are set up.
			Command: []string{"sh", "-l"},
			Stdin:   true,
			Stdout:  true,
			Stderr:  true,
			TTY:     true,
		}, scheme.ParameterCodec)
	return remotecommand.NewSPDYExecutor(h.Kube.Config, "POST", req.URL())
}

// pickPod selects the target pod: the named one when `want` is set
// and present, else the first Ready pod, else the first pod.
func pickPod(pods []projects.PodSummary, want string) *projects.PodSummary {
	if want != "" {
		for i := range pods {
			if pods[i].Name == want {
				return &pods[i]
			}
		}
		return nil
	}
	for i := range pods {
		if pods[i].Ready {
			return &pods[i]
		}
	}
	return &pods[0]
}

func (h *TerminalWSHandler) acquireSlot(userID string) bool {
	if userID == "" {
		userID = "_anon"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == nil {
		h.active = map[string]int{}
	}
	if h.active[userID] >= maxTerminalsPerUser {
		return false
	}
	h.active[userID]++
	return true
}

func (h *TerminalWSHandler) releaseSlot(userID string) {
	if userID == "" {
		userID = "_anon"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active != nil && h.active[userID] > 0 {
		h.active[userID]--
	}
}

// --- WS ↔ exec stream adapters --------------------------------------

// wsStdin implements io.Reader (stdin → pod) and
// remotecommand.TerminalSizeQueue (resize events). It reads WS frames:
// a JSON {"resize":{...}} frame is parsed as a resize; anything else
// is raw stdin bytes.
type wsStdin struct {
	conn   *websocket.Conn
	resize chan remotecommand.TerminalSize

	mu     sync.Mutex
	buf    []byte
	closed bool
	once   sync.Once
}

type resizeFrame struct {
	Resize *struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	} `json:"resize"`
}

func (s *wsStdin) Read(p []byte) (int, error) {
	for {
		// Drain any buffered bytes from a previous oversized frame.
		s.mu.Lock()
		if len(s.buf) > 0 {
			n := copy(p, s.buf)
			s.buf = s.buf[n:]
			s.mu.Unlock()
			return n, nil
		}
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return 0, websocket.ErrCloseSent
		}

		_, data, err := s.conn.ReadMessage()
		if err != nil {
			s.closeOnce()
			return 0, err
		}
		// A {"resize":...} JSON frame is a control message, not stdin.
		if len(data) > 0 && data[0] == '{' {
			var rf resizeFrame
			if json.Unmarshal(data, &rf) == nil && rf.Resize != nil {
				select {
				case s.resize <- remotecommand.TerminalSize{Width: rf.Resize.Cols, Height: rf.Resize.Rows}:
				default: // drop if the queue is full — next resize wins
				}
				continue
			}
		}
		n := copy(p, data)
		if n < len(data) {
			s.mu.Lock()
			s.buf = append(s.buf, data[n:]...)
			s.mu.Unlock()
		}
		return n, nil
	}
}

// Next implements remotecommand.TerminalSizeQueue.
func (s *wsStdin) Next() *remotecommand.TerminalSize {
	sz, ok := <-s.resize
	if !ok {
		return nil
	}
	return &sz
}

func (s *wsStdin) closeOnce() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.resize)
	})
}

// wsStdout implements io.Writer: pod stdout/stderr → WS binary frames.
type wsStdout struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsStdout) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
