package handlers

// portforward_ws.go hosts the addon port-forward WebSocket: a single
// raw TCP stream tunnelled from the operator's CLI through the kuso
// server to an addon pod, so a developer can run psql / TablePlus /
// any local tool against a cluster-internal database without the
// addon being publicly reachable.
//
//	GET /ws/projects/{project}/addons/{addon}/portforward
//
// The kuso server fronts the kube `pods/portforward` subresource so
// the browser/CLI never needs cluster credentials. Each WS connection
// proxies exactly ONE TCP connection — the client opens N WS
// connections for N concurrent psql tabs. That maps 1:1 to how
// kubectl port-forward multiplexes underneath and keeps this handler
// short.
//
// Wire protocol: raw binary frames in both directions. WS binary →
// pod stdin (the TCP byte stream). Pod stdout → WS binary.
//
// Admin-gated: addon port-forward is a privileged capability (it
// hands the caller a direct TCP channel to a managed database). The
// CLI/UI ALSO gate this; the server enforces independently.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/transport/spdy"

	"kuso/server/internal/addons"
	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// PortForwardWSHandler serves the addon TCP-tunnel WebSocket.
type PortForwardWSHandler struct {
	Svc    *addons.Service // namespace + addon CR resolution
	Kube   *kube.Client    // SPDY portforward transport
	Issuer *auth.Issuer
	DB     *db.DB
	Audit  *audit.Service
	Logger *slog.Logger

	// Per-user concurrency cap. A misbehaving CLI must not open
	// unbounded port-forward streams against the kube apiserver.
	mu     sync.Mutex
	active map[string]int
}

const maxPortForwardsPerUser = 8

// Mount registers the route on the public router (the WS handler
// rolls its own JWT + admin gate, like the logs/terminal WS).
func (h *PortForwardWSHandler) Mount(r chi.Router) {
	r.Get("/ws/projects/{project}/addons/{addon}/portforward", h.PortForward)
}

// PortForward upgrades to a WebSocket and tunnels one TCP connection
// through to the addon's primary pod on its Service's target port.
func (h *PortForwardWSHandler) PortForward(w http.ResponseWriter, r *http.Request) {
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
	// Revocation: Verify only checks signature + expiry. On the public
	// router (no auth middleware) we must consult the revocation hook
	// ourselves, else a revoked token still tunnels to a DB until expiry.
	if h.Issuer.CheckRevoked(r.Context(), claims) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Admin-only: a port-forward to a database is a strictly
	// elevated capability — not even the deployer role gets one.
	if !auth.Has(claims.Permissions, auth.PermSettingsAdmin) {
		http.Error(w, "forbidden: addon port-forward requires settings:admin", http.StatusForbidden)
		return
	}

	if !h.acquireSlot(claims.UserID) {
		http.Error(w, "too many concurrent port-forward sessions", http.StatusTooManyRequests)
		return
	}
	defer h.releaseSlot(claims.UserID)

	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")

	// Resolve the addon's Service: pick the Ready pod + targetPort.
	rctx, rcancel := context.WithTimeout(r.Context(), 10*time.Second)
	ns, podName, port, err := h.resolveAddonTarget(rctx, project, addon)
	rcancel()
	if err != nil {
		// 503: a missing/unhealthy addon is "not available right now",
		// not a 4xx client error.
		http.Error(w, "addon target not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

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
			Action:   "addon.portforward",
			Pipeline: project,
			App:      addon,
			Resource: "kuspod",
			Message:  fmt.Sprintf("port-forward opened to pod %s on :%d", podName, port),
		})
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn.SetCloseHandler(func(code int, text string) error {
		cancel()
		return nil
	})

	// Open the kube port-forward stream pair (data + error) for ONE
	// TCP connection. The kuso WS carries that data stream end-to-end.
	dataStream, errorStream, closeStreams, err := h.openPortForwardStreams(ctx, ns, podName, port)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("[kuso] portforward setup failed: "+err.Error()))
		return
	}
	defer closeStreams()

	// Drain the kube error stream into the logger (and force-close
	// the session on a non-empty error) — kubelet reports a stream
	// error here, not on the data stream.
	go func() {
		buf, rerr := io.ReadAll(errorStream)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			h.Logger.Warn("portforward error stream", "addon", addon, "err", rerr)
		}
		if len(buf) > 0 {
			h.Logger.Warn("portforward kube error", "addon", addon, "msg", string(buf))
			cancel()
		}
	}()

	// Bidirectional bridge: WS ↔ port-forward data stream.
	bridge(ctx, conn, dataStream, h.Logger)

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session closed"),
		time.Now().Add(2*time.Second),
	)
}

// resolveAddonTarget finds the namespace, a Ready pod, and the
// container target port for an addon. The Service for an addon is
// named after the addon CR (e.g. "distill-db"); we read its selector
// + first port and list pods matching.
func (h *PortForwardWSHandler) resolveAddonTarget(ctx context.Context, project, addon string) (ns, podName string, port int32, err error) {
	ns = h.Svc.NamespaceFor(ctx, project)
	fqn := h.Svc.AddonFQN(project, addon)

	svc, err := h.Kube.Clientset.CoreV1().Services(ns).Get(ctx, fqn, metav1.GetOptions{})
	if err != nil {
		return "", "", 0, fmt.Errorf("get service %s/%s: %w", ns, fqn, err)
	}
	if len(svc.Spec.Ports) == 0 {
		return "", "", 0, fmt.Errorf("service %s has no ports", fqn)
	}
	// targetPort is what the pod listens on; that's the port-forward
	// target. When it's a name (e.g. "postgres") we need to resolve
	// it against the pod's container port — pickPodForService does
	// that lookup at the same time as picking the pod.
	tp := svc.Spec.Ports[0].TargetPort
	if len(svc.Spec.Selector) == 0 {
		return "", "", 0, fmt.Errorf("service %s has no selector (headless?) — cannot resolve a pod", fqn)
	}
	sel := labelSelector(svc.Spec.Selector)
	pods, err := h.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return "", "", 0, fmt.Errorf("list pods %s: %w", sel, err)
	}
	var chosen *corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		if isPodReady(p) {
			chosen = p
			break
		}
	}
	if chosen == nil {
		return "", "", 0, fmt.Errorf("no Ready pod for service %s", fqn)
	}
	resolved, err := resolveTargetPort(chosen, tp)
	if err != nil {
		return "", "", 0, err
	}
	return ns, chosen.Name, resolved, nil
}

// openPortForwardStreams opens a single TCP-equivalent stream pair on
// the named pod's targetPort via the kube apiserver's
// pods/portforward subresource. Returns the data stream (bidirectional
// TCP-equivalent), the error stream (kubelet error messages — read
// separately so they can't intermix with the data path), and a
// closer that tears the whole connection down.
func (h *PortForwardWSHandler) openPortForwardStreams(ctx context.Context, ns, podName string, port int32) (data, errStream httpstream.Stream, closer func(), err error) {
	req := h.Kube.Clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Namespace(ns).
		Name(podName).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(h.Kube.Config)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("spdy transport: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())

	streamConn, _, err := dialer.Dial(portforwardProtocolV1Name)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial portforward: %w", err)
	}

	// Port-forward stream protocol: each TCP connection is a pair of
	// (data, error) streams identified by a "requestID" header and the
	// targeted port. We open one pair here per WS — the CLI opens N
	// WSes for N psql tabs.
	headers := http.Header{}
	headers.Set("streamType", "error")
	headers.Set("port", strconv.Itoa(int(port)))
	headers.Set("requestID", "0")
	errStream, err = streamConn.CreateStream(headers)
	if err != nil {
		streamConn.Close()
		return nil, nil, nil, fmt.Errorf("create error stream: %w", err)
	}

	headers.Set("streamType", "data")
	data, err = streamConn.CreateStream(headers)
	if err != nil {
		streamConn.Close()
		return nil, nil, nil, fmt.Errorf("create data stream: %w", err)
	}

	closer = func() {
		_ = data.Close()
		_ = errStream.Close()
		_ = streamConn.Close()
		_ = ctx.Err() // silence unused
	}
	return data, errStream, closer, nil
}

// portforwardProtocolV1Name is kube's port-forward subprotocol id.
const portforwardProtocolV1Name = "portforward.k8s.io"

// bridge proxies bytes between the WebSocket and the kube data
// stream until either side closes.
func bridge(ctx context.Context, ws *websocket.Conn, ds io.ReadWriteCloser, logger *slog.Logger) {
	done := make(chan struct{}, 2)

	// pod → ws
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, err := ds.Read(buf)
			if n > 0 {
				if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					logger.Debug("portforward read", "err", err)
				}
				return
			}
		}
	}()

	// ws → pod
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if len(msg) == 0 {
				continue
			}
			if _, werr := ds.Write(msg); werr != nil {
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
	case <-done:
	}
}

// resolveTargetPort turns a Service.targetPort (which can be an int
// or a port-name like "postgres") into the int port the pod listens
// on. Mirrors the kube lookup kubectl port-forward does.
func resolveTargetPort(pod *corev1.Pod, tp intstr.IntOrString) (int32, error) {
	if tp.Type == intstr.Int {
		return tp.IntVal, nil
	}
	name := tp.StrVal
	for _, c := range pod.Spec.Containers {
		for _, p := range c.Ports {
			if p.Name == name {
				return p.ContainerPort, nil
			}
		}
	}
	return 0, fmt.Errorf("targetPort %q not found in pod %s containers", name, pod.Name)
}

// labelSelector turns a Service.Selector map into a comma-separated
// key=value string suitable for ListOptions.LabelSelector.
func labelSelector(sel map[string]string) string {
	out := ""
	for k, v := range sel {
		if out != "" {
			out += ","
		}
		out += k + "=" + v
	}
	return out
}

// isPodReady reports whether the pod has a Ready condition set to
// True. A pod can be Running but not Ready (still passing startup
// probes) — port-forwarding to it would hit a refused connection.
func isPodReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (h *PortForwardWSHandler) acquireSlot(userID string) bool {
	if userID == "" {
		userID = "_anon"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == nil {
		h.active = map[string]int{}
	}
	if h.active[userID] >= maxPortForwardsPerUser {
		return false
	}
	h.active[userID]++
	return true
}

func (h *PortForwardWSHandler) releaseSlot(userID string) {
	if userID == "" {
		userID = "_anon"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active != nil && h.active[userID] > 0 {
		h.active[userID]--
	}
}

