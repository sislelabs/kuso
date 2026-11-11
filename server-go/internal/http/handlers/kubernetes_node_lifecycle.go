package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	"kuso/server/internal/audit"
	"kuso/server/internal/kube"
	"kuso/server/internal/nodejoin"
	"kuso/server/internal/nodewatch"
)

// Mutating node lifecycle: SSH-driven join, pre-flight validate,
// remove (cordon → drain → delete → optional uninstall), and the
// label PUT that round-trips through reconcileRegionTaint. Every
// route here is admin-gated; the read-only views in
// kubernetes_nodes.go are open to any authenticated user.

// kusoNodeLabels moves join labels into the kuso.sislelabs.com/
// namespace, as the bootstrap path does, so placement and region chips
// see them. Keys that already carry a prefix (kuso's or another) pass
// through unchanged.
func kusoNodeLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if !strings.Contains(k, "/") {
			k = kusoLabelPrefix + k
		}
		out[k] = v
	}
	return out
}

// JoinNode runs the SSH-driven k3s agent install on a remote VM.
// Body: {host, user, password|privateKey, port?, labels?, name?}.
// Returns the install output verbatim so the user can debug install
// errors in the modal. Synchronous: the request blocks for the
// duration of the install (typically 30-90s), so the http server
// keeps a long-poll context.
func (h *KubernetesHandler) JoinNode(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Kube == nil {
		writeErr(w, http.StatusServiceUnavailable, "kube client not wired")
		return
	}
	var body struct {
		nodejoin.Credentials
		SSHKeyID string            `json:"sshKeyId,omitempty"`
		Labels   map[string]string `json:"labels"`
		Name     string            `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	creds, err := h.resolveCreds(r.Context(), body.Credentials, body.SSHKeyID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	body.Credentials = creds
	if err := nodejoin.CheckJoinTokenPolicy(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	token, err := nodejoin.ReadServerToken()
	if err != nil {
		h.Logger.Error("read k3s token", "err", err)
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	k3sURL := controlPlaneJoinURL()
	if k3sURL == "" {
		writeErr(w, http.StatusServiceUnavailable, "could not derive control-plane URL — set KUSO_K3S_URL=https://<host>:6443 on the kuso-server deployment")
		return
	}
	// 3-minute ceiling for the whole flow. Reachability probe + apt
	// update + k3s download + service start: 90s typical, 3 min keeps
	// pathological cases (slow mirror) from hanging the request.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := nodejoin.Join(ctx, nodejoin.JoinSpec{
		Credentials: body.Credentials,
		K3sURL:      k3sURL,
		K3sToken:    token,
		NodeLabels:  kusoNodeLabels(body.Labels),
		NodeName:    body.Name,
		K3sVersion:  nodejoin.ServerK3sVersion(h.Kube.Clientset),
	})
	if err != nil {
		// Return 502 so the UI distinguishes "the join failed on the
		// remote host" from a kuso-side 500.
		out := ""
		if res != nil {
			out = res.Output
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  err.Error(),
			"output": out,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"output":   res.Output,
		"nodeName": res.NodeName,
	})
}

// ValidateNode runs the pre-flight Coolify-style check: SSH handshake,
// root/sudo, control-plane reachability, curl available, k3s presence.
// Returns one entry per check so the UI can render a tidy list.
func (h *KubernetesHandler) ValidateNode(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		nodejoin.Credentials
		SSHKeyID string `json:"sshKeyId,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	creds, err := h.resolveCreds(r.Context(), body.Credentials, body.SSHKeyID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	k3sURL := controlPlaneJoinURL()
	if k3sURL == "" {
		writeErr(w, http.StatusServiceUnavailable, "could not derive control-plane URL")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := nodejoin.Validate(ctx, creds, k3sURL)
	if err != nil {
		h.Logger.Error("validate node", "err", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// resolveCreds fills in private-key bytes from the SSH key library
// when sshKeyId is present. Falls through to whatever the caller
// supplied directly (password / inline private key) otherwise.
func (h *KubernetesHandler) resolveCreds(ctx context.Context, creds nodejoin.Credentials, sshKeyID string) (nodejoin.Credentials, error) {
	if sshKeyID == "" {
		return creds, nil
	}
	if h.DB == nil {
		return creds, fmt.Errorf("ssh key library not wired")
	}
	key, err := h.DB.GetSSHKey(ctx, sshKeyID)
	if err != nil {
		return creds, fmt.Errorf("ssh key %s: %w", sshKeyID, err)
	}
	creds.PrivateKey = key.PrivateKey
	// Don't clear creds.Password — the operator might prefer to use
	// the key for the install but keep the host password set as a
	// fallback. We just prefer the key.
	return creds, nil
}

// RemoveNode cordons + drains + deletes the node from kube. When
// credentials are supplied it also runs k3s-agent-uninstall.sh over
// SSH so the host is left clean. Without creds the node is removed
// from the control plane only — the VM continues to exist as a dead
// agent (operator's call: maybe the VM is already gone).
func (h *KubernetesHandler) RemoveNode(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	name := chiURLParam(r, "name")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "missing node name")
		return
	}
	if h.Kube == nil || h.Kube.Clientset == nil {
		writeErr(w, http.StatusServiceUnavailable, "kube client not wired")
		return
	}
	// Refuse to remove the control plane — we'd kill ourselves. This
	// is the cheap version; a future "Replace control plane" flow can
	// override.
	live, err := h.Kube.Clientset.CoreV1().Nodes().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeErr(w, http.StatusNotFound, "node not found: "+err.Error())
		return
	}
	if _, isCP := live.Labels["node-role.kubernetes.io/control-plane"]; isCP {
		// Only refuse when this would leave the cluster with zero
		// control-plane nodes. Multi-master users replacing a master
		// (e.g. host failure, hardware refresh) need this path; the
		// blanket refuse-control-plane gate locked them out.
		nodes, lerr := h.Kube.Clientset.CoreV1().Nodes().List(r.Context(), metav1.ListOptions{
			LabelSelector: "node-role.kubernetes.io/control-plane",
		})
		if lerr != nil {
			writeErr(w, http.StatusBadGateway, "preflight: "+lerr.Error())
			return
		}
		if len(nodes.Items) <= 1 {
			writeErr(w, http.StatusForbidden, "refusing to remove the last control-plane node — would leave the cluster headless")
			return
		}
	}

	// Node-local volumes (local-path) die with the node. Refuse unless
	// the caller explicitly accepts that with ?force=true (distinct from
	// the body's drain force).
	pinned, err := h.listPinnedVolumes(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "preflight: "+err.Error())
		return
	}
	if len(pinned) > 0 && r.URL.Query().Get("force") != "true" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  fmt.Sprintf("%d volume(s) store data only on %s — move or back them up, or retry with force=true", len(pinned), name),
			"pinned": pinned,
		})
		return
	}

	body := struct {
		Credentials *nodejoin.Credentials `json:"credentials"`
		Force       bool                  `json:"force"`
	}{}
	_ = json.NewDecoder(r.Body).Decode(&body) // body is optional

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	// Draining our own node would kill this request mid-way and leave the
	// node cordoned but not deleted.
	if self, _ := os.Hostname(); self != "" {
		ns := h.Namespace
		if ns == "" {
			ns = "kuso"
		}
		if p, err := h.Kube.Clientset.CoreV1().Pods(ns).Get(ctx, self, metav1.GetOptions{}); err == nil && p.Spec.NodeName == name {
			writeErr(w, http.StatusConflict, fmt.Sprintf(
				"kuso-server (%s) is running on %s and can't drain its own node; cordon %s and restart kuso-server so it moves, then retry", self, name, name))
			return
		}
	}
	// Cordon first so kube stops scheduling new pods onto the node.
	if err := cordonNode(ctx, h.Kube, name, true); err != nil {
		h.Logger.Warn("cordon", "node", name, "err", err)
	}
	// Drain: evict every non-DaemonSet pod (PDBs respected) and wait for
	// them to go. Force=true deletes them with no grace period instead.
	if err := drainNode(ctx, h.Kube, name, body.Force); err != nil && !body.Force {
		writeErr(w, http.StatusBadGateway, "drain failed: "+err.Error())
		return
	}
	if err := h.Kube.Clientset.CoreV1().Nodes().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		writeErr(w, http.StatusBadGateway, "delete node: "+err.Error())
		return
	}
	// Best-effort: also clean up the host so the VM isn't left in a
	// broken half-installed state.
	uninstallOut := ""
	if body.Credentials != nil && body.Credentials.Host != "" {
		out, uerr := nodejoin.Uninstall(ctx, *body.Credentials)
		uninstallOut = out
		if uerr != nil {
			h.Logger.Warn("uninstall ssh", "node", name, "err", uerr)
		}
	}
	msg := "node removed"
	if len(pinned) > 0 {
		msg = fmt.Sprintf("node removed with %d node-local volume(s) stranded", len(pinned))
	}
	h.Audit.Log(ctx, audit.Entry{
		User:     auditUser(ctx),
		Severity: "critical",
		Action:   "node.remove",
		Resource: name,
		Message:  msg,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"removed":      name,
		"uninstallOut": uninstallOut,
	})
}

// PutNodeLabels replaces the kuso-managed labels for a node. Body is
// {labels: {key: value}} — bare keys (no namespace prefix). The
// server applies the kuso.sislelabs.com/ prefix on the way in and
// strips it on the way out via Nodes() so the user never sees the
// namespace mechanics.
//
// Convention: when the user sets `region`, the server also drops a
// matching NoSchedule taint so workloads without a matching
// toleration won't land here. Removing the region label removes
// the matching taint. Other labels are pure metadata for now;
// future placement logic can pin services to specific
// labels via spec.placement.
func (h *KubernetesHandler) PutNodeLabels(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	name := chiURLParam(r, "name")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "missing node name")
		return
	}
	var body struct {
		Labels map[string]string `json:"labels"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if body.Labels == nil {
		body.Labels = map[string]string{}
	}
	for k, v := range body.Labels {
		if k == "" {
			writeErr(w, http.StatusBadRequest, "label key cannot be empty")
			return
		}
		// Validate here so a bad key or value is a 400 naming the
		// problem, not a 500 from the apiserver's rejection.
		if errs := validation.IsQualifiedName(kusoLabelPrefix + k); len(errs) > 0 {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("label key %q: %s", k, strings.Join(errs, "; ")))
			return
		}
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("label %q value %q: %s", k, v, strings.Join(errs, "; ")))
			return
		}
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()

	// Read the live node so we can compute the diff (which kuso labels
	// went away) and translate that into a minimal label patch +
	// matching taint diff.
	live, err := h.Kube.Clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		writeErr(w, http.StatusNotFound, "node "+name+" not found")
		return
	}
	if err != nil {
		h.Logger.Error("get node for label put", "node", name, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	// Step 1 — labels patch. Set every key in body; delete any kuso-
	// namespaced label that's no longer in body. JSON merge-patch
	// uses null to delete a key.
	desired := map[string]any{}
	for k, v := range body.Labels {
		desired[kusoLabelPrefix+k] = v
	}
	for k := range live.Labels {
		if len(k) > len(kusoLabelPrefix) && k[:len(kusoLabelPrefix)] == kusoLabelPrefix {
			short := k[len(kusoLabelPrefix):]
			if _, keep := body.Labels[short]; !keep {
				desired[k] = nil
			}
		}
	}
	labelPatch, err := json.Marshal(map[string]any{"metadata": map[string]any{"labels": desired}})
	if err != nil {
		h.Logger.Error("marshal node label patch", "node", name, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	if _, err := h.Kube.Clientset.CoreV1().Nodes().Patch(
		ctx, name, "application/merge-patch+json", labelPatch, metav1.PatchOptions{},
	); err != nil {
		if status, ok := kubeErrStatus(err); ok {
			writeErr(w, status, err.Error())
			return
		}
		h.Logger.Error("patch node labels", "node", name, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	// Step 2 — region-derived taint. Re-fetch so we see the new label
	// state, then ensure spec.taints carries exactly one
	// kuso.sislelabs.com/region=<value>:NoSchedule taint matching the
	// current label (or none, if the label is gone).
	if err := h.reconcileRegionTaint(ctx, name, body.Labels["region"]); err != nil {
		// The labels landed but the matching taint didn't, so pods
		// without the region toleration can still schedule there. Say
		// so instead of a bare 204.
		h.Logger.Warn("reconcile region taint", "node", name, "err", err)
		writeJSON(w, http.StatusOK, map[string]string{
			"warning": "labels applied, but updating the region taint failed: " + err.Error(),
		})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// reconcileRegionTaint ensures spec.taints contains exactly one kuso
// region taint matching desiredValue (empty string = remove the taint).
// We send the full taint list so SMP doesn't merge — which is the
// only way to make a kube taint go away via patch.
func (h *KubernetesHandler) reconcileRegionTaint(ctx context.Context, name, desiredValue string) error {
	const taintKey = kusoLabelPrefix + "region"
	live, err := h.Kube.Clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	out := []map[string]any{}
	for _, t := range live.Spec.Taints {
		if t.Key == taintKey {
			continue
		}
		out = append(out, map[string]any{"key": t.Key, "value": t.Value, "effect": string(t.Effect), "timeAdded": t.TimeAdded})
	}
	if desiredValue != "" {
		out = append(out, map[string]any{
			"key":    taintKey,
			"value":  desiredValue,
			"effect": "NoSchedule",
		})
	}
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{"$retainKeys": []string{"taints"}, "taints": out},
	})
	if err != nil {
		return err
	}
	_, err = h.Kube.Clientset.CoreV1().Nodes().Patch(
		ctx, name, "application/strategic-merge-patch+json", patch, metav1.PatchOptions{},
	)
	return err
}

// controlPlaneJoinURL derives the URL agents should hit. Operators can
// pin it via env (KUSO_K3S_URL) when the in-cluster apiserver IP isn't
// the right one to advertise to remote VMs (e.g. behind NAT or split-
// horizon DNS). Default: KUBERNETES_SERVICE_HOST + 6443. Note: that's
// the in-cluster ClusterIP, only useful when the new node is on the
// same network. For Hetzner-cloud-style joins the operator should set
// KUSO_K3S_URL=https://<public-host>:6443.
func controlPlaneJoinURL() string {
	if v := os.Getenv("KUSO_K3S_URL"); v != "" {
		return v
	}
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	if host == "" {
		return ""
	}
	// Force port 6443 — the apiserver listens there even though the
	// in-cluster Service often advertises 443.
	return "https://" + host + ":6443"
}

// cordonNode toggles spec.unschedulable. Same JSON-patch pattern the
// existing PutNodeLabels handler uses.
func cordonNode(ctx context.Context, k *kube.Client, name string, on bool) error {
	patch := []byte(fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, on))
	_, err := k.Clientset.CoreV1().Nodes().Patch(
		ctx, name,
		types.MergePatchType,
		patch,
		metav1.PatchOptions{},
	)
	return err
}

// Drain budgets. Vars so tests can shrink them. Both sit well inside
// RemoveNode's 2-minute request context.
var (
	// evictRetryFor bounds how long one PDB-blocked eviction is retried.
	evictRetryFor = 30 * time.Second
	// drainGoneWait bounds the wait for evicted pods to terminate before
	// the Node object is deleted.
	drainGoneWait = 60 * time.Second
)

// drainablePods lists the pods on node that a drain must move: not
// DaemonSet-managed (they come right back) and not mirror pods (static
// manifests the kubelet owns).
func drainablePods(ctx context.Context, k *kube.Client, name string) ([]corev1.Pod, error) {
	pods, err := k.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + name,
	})
	if err != nil {
		return nil, err
	}
	var out []corev1.Pod
	for _, p := range pods.Items {
		if p.Spec.NodeName != name {
			continue
		}
		isDaemon := false
		for _, owner := range p.OwnerReferences {
			if owner.Kind == "DaemonSet" {
				isDaemon = true
				break
			}
		}
		if isDaemon {
			continue
		}
		if _, ok := p.Annotations["kubernetes.io/config.mirror"]; ok {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// drainNode moves every drainable pod off the node and waits for them
// to terminate. Without force it uses the Eviction API, so
// PodDisruptionBudgets hold: a blocked eviction is retried for
// evictRetryFor, then the drain fails with a clear error instead of
// deleting the protected pod. If kuso-server lacks the pods/eviction
// grant it falls back to a graceful delete (the pre-eviction behaviour).
// With force it deletes with a zero grace period and ignores errors.
func drainNode(ctx context.Context, k *kube.Client, name string, force bool) error {
	pods, err := drainablePods(ctx, k, name)
	if err != nil {
		return fmt.Errorf("list pods on node: %w", err)
	}
	for i := range pods {
		p := &pods[i]
		if force {
			zero := int64(0)
			_ = k.Clientset.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
			continue
		}
		if err := evictPod(ctx, k, p); err != nil {
			return err
		}
	}
	if force {
		return nil
	}
	deadline := time.Now().Add(drainGoneWait)
	for {
		left, err := drainablePods(ctx, k, name)
		if err == nil && len(left) == 0 {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if err != nil {
				return fmt.Errorf("drain: could not confirm pods left %s: %w", name, err)
			}
			return fmt.Errorf("drain: %d pod(s) still terminating on %s after %s (e.g. %s/%s); retry once they're gone",
				len(left), name, drainGoneWait, left[0].Namespace, left[0].Name)
		}
		time.Sleep(time.Second)
	}
}

// evictPod evicts one pod, retrying while a PodDisruptionBudget refuses.
func evictPod(ctx context.Context, k *kube.Client, p *corev1.Pod) error {
	ev := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace}}
	deadline := time.Now().Add(evictRetryFor)
	for {
		err := k.Clientset.PolicyV1().Evictions(p.Namespace).Evict(ctx, ev)
		switch {
		case err == nil, apierrors.IsNotFound(err):
			return nil
		case apierrors.IsForbidden(err):
			slog.Warn("drain: no pods/eviction grant; deleting pod without PodDisruptionBudget checks — re-apply the kuso-server ClusterRole",
				"pod", p.Namespace+"/"+p.Name)
			if derr := k.Clientset.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				return fmt.Errorf("evict %s/%s: %w", p.Namespace, p.Name, derr)
			}
			return nil
		case apierrors.IsTooManyRequests(err):
			if time.Now().After(deadline) || ctx.Err() != nil {
				return fmt.Errorf("evict %s/%s: blocked by its pod disruption budget for %s; scale the workload up or retry with force: %w",
					p.Namespace, p.Name, evictRetryFor, err)
			}
		default:
			return fmt.Errorf("evict %s/%s: %w", p.Namespace, p.Name, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("evict %s/%s: %w", p.Namespace, p.Name, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// PinnedVolume is a PVC whose PersistentVolume is node-local storage on
// the node being removed. Removing the node strands its data.
type PinnedVolume struct {
	Namespace string `json:"namespace"`
	PVC       string `json:"pvc"`
	// Kind is "addon", "volume" (a service volume) or "other".
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
	// Owner is the addon CR name or "<service>/<volume>".
	Owner string `json:"owner,omitempty"`
	Size  string `json:"size,omitempty"`
}

// selectedNodeAnnotation is stamped on a claim by the scheduler when its
// volume is provisioned for a node (WaitForFirstConsumer, as local-path
// does). kuso-server can't list PersistentVolumes — they're cluster-scoped
// and outside its RBAC, which the updater can't widen — so the claim's
// annotation is how we know which node holds the data.
const selectedNodeAnnotation = "volume.kubernetes.io/selected-node"

// pinnedVolumesOnNode returns the claims whose volume was provisioned on
// node. Over-reports network storage bound under WaitForFirstConsumer,
// which is the safe direction for a removal guard.
func pinnedVolumesOnNode(pvcs []corev1.PersistentVolumeClaim, node string) []PinnedVolume {
	var out []PinnedVolume
	for i := range pvcs {
		pvc := &pvcs[i]
		if pvc.Annotations[selectedNodeAnnotation] != node {
			continue
		}
		pin := PinnedVolume{Namespace: pvc.Namespace, PVC: pvc.Name, Kind: "other"}
		if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
			pin.Size = q.String()
		}
		l := pvc.Labels
		pin.Project = l["kuso.sislelabs.com/project"]
		switch {
		case l["app.kubernetes.io/name"] == "kusoaddon":
			pin.Kind = "addon"
			pin.Owner = l["app.kubernetes.io/instance"]
		case l["kuso.sislelabs.com/volume"] != "":
			pin.Kind = "volume"
			pin.Owner = l["kuso.sislelabs.com/service"] + "/" + l["kuso.sislelabs.com/volume"]
		}
		out = append(out, pin)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Namespace != out[b].Namespace {
			return out[a].Namespace < out[b].Namespace
		}
		return out[a].PVC < out[b].PVC
	})
	return out
}

func (h *KubernetesHandler) listPinnedVolumes(ctx context.Context, node string) ([]PinnedVolume, error) {
	pvcList, err := h.Kube.Clientset.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list persistent volume claims: %w", err)
	}
	return pinnedVolumesOnNode(pvcList.Items, node), nil
}

// RemoveNodePreflight lists the data that lives only on this node.
//
// GET /api/kubernetes/nodes/{name}/remove-preflight → {"pinned":[PinnedVolume]}
func (h *KubernetesHandler) RemoveNodePreflight(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Kube == nil || h.Kube.Clientset == nil {
		writeErr(w, http.StatusServiceUnavailable, "kube client not wired")
		return
	}
	pinned, err := h.listPinnedVolumes(r.Context(), chiURLParam(r, "name"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if pinned == nil {
		pinned = []PinnedVolume{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pinned": pinned})
}

// UncordonNode makes a node schedulable again and drops the nodewatch
// marker, so the watcher no longer treats it as a node it cordoned.
//
// POST /api/kubernetes/nodes/{name}/uncordon
func (h *KubernetesHandler) UncordonNode(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Kube == nil || h.Kube.Clientset == nil {
		writeErr(w, http.StatusServiceUnavailable, "kube client not wired")
		return
	}
	name := chiURLParam(r, "name")
	patch := []byte(`{"spec":{"unschedulable":false},"metadata":{"annotations":{"` + nodewatch.CordonAnnotation + `":null}}}`)
	if _, err := h.Kube.Clientset.CoreV1().Nodes().Patch(r.Context(), name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "node not found")
			return
		}
		writeErr(w, http.StatusBadGateway, "uncordon: "+err.Error())
		return
	}
	h.Audit.Log(r.Context(), audit.Entry{
		User:     auditUser(r.Context()),
		Severity: "warn",
		Action:   "node.uncordon",
		Resource: name,
		Message:  "node uncordoned",
	})
	writeJSON(w, http.StatusOK, map[string]string{"uncordoned": name})
}
