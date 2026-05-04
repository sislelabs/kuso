package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
	"kuso/server/internal/nodejoin"
)

// Mutating node lifecycle: SSH-driven join, pre-flight validate,
// remove (cordon → drain → delete → optional uninstall), and the
// label PUT that round-trips through reconcileRegionTaint. Every
// route here is admin-gated; the read-only views in
// kubernetes_nodes.go are open to any authenticated user.

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
		http.Error(w, "kube client not wired", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		nodejoin.Credentials
		SSHKeyID string            `json:"sshKeyId,omitempty"`
		Labels   map[string]string `json:"labels"`
		Name     string            `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	creds, err := h.resolveCreds(r.Context(), body.Credentials, body.SSHKeyID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body.Credentials = creds
	token, err := nodejoin.ReadServerToken()
	if err != nil {
		h.Logger.Error("read k3s token", "err", err)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	k3sURL := controlPlaneJoinURL()
	if k3sURL == "" {
		http.Error(w, "could not derive control-plane URL — set KUSO_K3S_URL=https://<host>:6443 on the kuso-server deployment", http.StatusServiceUnavailable)
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
		NodeLabels:  body.Labels,
		NodeName:    body.Name,
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
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	creds, err := h.resolveCreds(r.Context(), body.Credentials, body.SSHKeyID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	k3sURL := controlPlaneJoinURL()
	if k3sURL == "" {
		http.Error(w, "could not derive control-plane URL", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := nodejoin.Validate(ctx, creds, k3sURL)
	if err != nil {
		h.Logger.Error("validate node", "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
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
		http.Error(w, "missing node name", http.StatusBadRequest)
		return
	}
	if h.Kube == nil || h.Kube.Clientset == nil {
		http.Error(w, "kube client not wired", http.StatusServiceUnavailable)
		return
	}
	// Refuse to remove the control plane — we'd kill ourselves. This
	// is the cheap version; a future "Replace control plane" flow can
	// override.
	live, err := h.Kube.Clientset.CoreV1().Nodes().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		http.Error(w, "node not found: "+err.Error(), http.StatusNotFound)
		return
	}
	if _, isCP := live.Labels["node-role.kubernetes.io/control-plane"]; isCP {
		http.Error(w, "refusing to remove the control-plane node", http.StatusForbidden)
		return
	}

	body := struct {
		Credentials *nodejoin.Credentials `json:"credentials"`
		Force       bool                  `json:"force"`
	}{}
	_ = json.NewDecoder(r.Body).Decode(&body) // body is optional

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	// Cordon first so kube stops scheduling new pods onto the node.
	if err := cordonNode(ctx, h.Kube, name, true); err != nil {
		h.Logger.Warn("cordon", "node", name, "err", err)
	}
	// Drain: evict every non-DaemonSet pod. We don't replicate
	// kubectl-drain's full logic (PDB respect, etc.) — simple eviction
	// loop is enough for a 2-3 node home cluster. Force=true skips
	// pods that won't evict gracefully.
	if err := drainNode(ctx, h.Kube, name, body.Force); err != nil && !body.Force {
		http.Error(w, "drain failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := h.Kube.Clientset.CoreV1().Nodes().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		http.Error(w, "delete node: "+err.Error(), http.StatusBadGateway)
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
		http.Error(w, "missing node name", http.StatusBadRequest)
		return
	}
	var body struct {
		Labels map[string]string `json:"labels"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Labels == nil {
		body.Labels = map[string]string{}
	}
	for k := range body.Labels {
		if k == "" {
			http.Error(w, "label key cannot be empty", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()

	// Read the live node so we can compute the diff (which kuso labels
	// went away) and translate that into a minimal label patch +
	// matching taint diff.
	live, err := h.Kube.Clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		h.Logger.Error("get node for label put", "node", name, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
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
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if _, err := h.Kube.Clientset.CoreV1().Nodes().Patch(
		ctx, name, "application/merge-patch+json", labelPatch, metav1.PatchOptions{},
	); err != nil {
		h.Logger.Error("patch node labels", "node", name, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	// Step 2 — region-derived taint. Re-fetch so we see the new label
	// state, then ensure spec.taints carries exactly one
	// kuso.sislelabs.com/region=<value>:NoSchedule taint matching the
	// current label (or none, if the label is gone).
	if err := h.reconcileRegionTaint(ctx, name, body.Labels["region"]); err != nil {
		h.Logger.Warn("reconcile region taint", "node", name, "err", err)
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

// drainNode evicts all non-DaemonSet, non-mirror pods from the node.
// Returns the first eviction error unless force=true (then we log and
// continue). Simple sequential loop — fine for a small node.
func drainNode(ctx context.Context, k *kube.Client, name string, force bool) error {
	pods, err := k.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + name,
	})
	if err != nil {
		return fmt.Errorf("list pods on node: %w", err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		// Skip DaemonSet-managed pods (they'll come right back) and
		// mirror pods (static manifests on the host — kubelet owns
		// them, control plane can't evict).
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
		// Use Delete with a 0 grace period when force; otherwise rely
		// on the pod's terminationGracePeriodSeconds so workloads can
		// shut down cleanly.
		opts := metav1.DeleteOptions{}
		if force {
			zero := int64(0)
			opts.GracePeriodSeconds = &zero
		}
		if err := k.Clientset.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, opts); err != nil && !force {
			return fmt.Errorf("evict %s/%s: %w", p.Namespace, p.Name, err)
		}
	}
	return nil
}
