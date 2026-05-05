// Package platformharden ensures the cluster's control-plane
// deployments (traefik, cert-manager, kuso-operator) have probe
// timings + resource requests that survive build-time noise on a
// small node.
//
// Why: when a kaniko build saturates the 2-vCPU node, default kube
// probes (3-failure × 10s = 30s grace) fire long before nix-env
// finishes. Traefik gets killed mid-build → ingress dies → the
// dashboard goes ERR_CONNECTION_REFUSED. We give every long-lived
// platform pod the same tolerant-probe + Burstable-QoS treatment
// kuso-server has in deploy/server-go.yaml.
//
// Idempotent: safe to run on every kuso-server boot. Only patches
// when the existing config is below the floor; never overrides
// operator-tuned higher values.
package platformharden

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// Target identifies a control-plane deployment we harden plus the
// container name within it.
type Target struct {
	Namespace, Deployment, ContainerName string
	// CPU / mem floors. Never lowered, only raised. Operators can
	// always tune up by hand and we won't overwrite.
	CPURequest, MemRequest, CPULimit, MemLimit string
	// Fallback probe path if we have to write a probe from scratch
	// (the deployment shipped without one). Empty = skip.
	ProbePath string
	ProbePort string
}

var defaults = []Target{
	{
		Namespace:     "traefik",
		Deployment:    "traefik",
		ContainerName: "traefik",
		CPURequest:    "100m",
		MemRequest:    "96Mi",
		CPULimit:      "500m",
		MemLimit:      "256Mi",
		ProbePath:     "/ping",
		ProbePort:     "8080",
	},
	{
		Namespace:     "kuso-operator-system",
		Deployment:    "kuso-operator-controller-manager",
		ContainerName: "manager",
		CPURequest:    "100m",
		MemRequest:    "192Mi",
		CPULimit:      "1000m",
		MemLimit:      "768Mi",
	},
	{
		Namespace:     "cert-manager",
		Deployment:    "cert-manager",
		ContainerName: "cert-manager-controller",
		CPURequest:    "50m",
		MemRequest:    "96Mi",
		CPULimit:      "200m",
		MemLimit:      "256Mi",
	},
	{
		Namespace:     "cert-manager",
		Deployment:    "cert-manager-webhook",
		ContainerName: "cert-manager-webhook",
		CPURequest:    "20m",
		MemRequest:    "64Mi",
		CPULimit:      "100m",
		MemLimit:      "128Mi",
	},
	{
		Namespace:     "cert-manager",
		Deployment:    "cert-manager-cainjector",
		ContainerName: "cert-manager-cainjector",
		CPURequest:    "20m",
		MemRequest:    "64Mi",
		CPULimit:      "100m",
		MemLimit:      "128Mi",
	},
}

// Floor: the minimum-acceptable probe shape. failureThreshold=6 ×
// periodSeconds=30 = 3 minutes of grace before kubelet kills the pod.
// Matches kuso-server's probes in deploy/server-go.yaml.
const (
	floorPeriodSeconds    int32 = 30
	floorFailureThreshold int32 = 6
	floorTimeoutSeconds   int32 = 5
	floorInitialDelay     int32 = 15
	floorReadinessPeriod  int32 = 10
)

// Run applies the hardening once. Best-effort: per-target failures
// log a warn and we move on. Caller doesn't block on this.
func Run(ctx context.Context, kc *kube.Client, logger *slog.Logger) {
	if kc == nil {
		return
	}
	for _, t := range defaults {
		hardenOne(ctx, kc, t, logger)
	}
}

func hardenOne(ctx context.Context, kc *kube.Client, t Target, logger *slog.Logger) {
	lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	dep, err := kc.Clientset.AppsV1().Deployments(t.Namespace).Get(lctx, t.Deployment, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		logger.Debug("platformharden: not found, skipping", "ns", t.Namespace, "deploy", t.Deployment)
		return
	}
	if err != nil {
		logger.Warn("platformharden: get", "ns", t.Namespace, "deploy", t.Deployment, "err", err)
		return
	}
	patch, fields := buildPatch(dep, t)
	if patch == nil {
		return
	}
	pb, err := json.Marshal(patch)
	if err != nil {
		logger.Warn("platformharden: marshal", "deploy", t.Deployment, "err", err)
		return
	}
	if _, err := kc.Clientset.AppsV1().Deployments(t.Namespace).Patch(
		lctx, t.Deployment, types.StrategicMergePatchType, pb, metav1.PatchOptions{},
	); err != nil {
		logger.Warn("platformharden: patch", "deploy", t.Deployment, "err", err)
		return
	}
	logger.Info("platformharden: hardened",
		"ns", t.Namespace, "deploy", t.Deployment, "fields", fields)
}

// buildPatch computes the strategic-merge patch needed to bring the
// deployment up to floor. Returns nil + empty fields if nothing needs
// changing.
func buildPatch(dep *appsv1.Deployment, t Target) (map[string]any, []string) {
	c := findContainer(dep, t.ContainerName)
	if c == nil {
		return nil, nil
	}
	patchContainer := map[string]any{"name": t.ContainerName}
	fields := []string{}

	// Probes — only patch if probe exists AND is stricter than floor.
	// We don't fabricate a probe where none exists; the operator
	// presumably disabled it deliberately.
	if needsProbeUpgrade(c.LivenessProbe) {
		patchContainer["livenessProbe"] = probePatch(c.LivenessProbe, t, false)
		fields = append(fields, "livenessProbe")
	}
	if needsProbeUpgrade(c.ReadinessProbe) {
		patchContainer["readinessProbe"] = probePatch(c.ReadinessProbe, t, true)
		fields = append(fields, "readinessProbe")
	}

	// Resources. Strategic merge respects sub-keys, so we only emit
	// the ones we want to change.
	resPatch := map[string]map[string]string{}
	if needRaise(c.Resources.Requests, corev1.ResourceCPU, t.CPURequest) {
		ensure(resPatch, "requests")["cpu"] = t.CPURequest
	}
	if needRaise(c.Resources.Requests, corev1.ResourceMemory, t.MemRequest) {
		ensure(resPatch, "requests")["memory"] = t.MemRequest
	}
	if needRaise(c.Resources.Limits, corev1.ResourceCPU, t.CPULimit) {
		ensure(resPatch, "limits")["cpu"] = t.CPULimit
	}
	if needRaise(c.Resources.Limits, corev1.ResourceMemory, t.MemLimit) {
		ensure(resPatch, "limits")["memory"] = t.MemLimit
	}
	if len(resPatch) > 0 {
		patchContainer["resources"] = resPatch
		fields = append(fields, "resources")
	}

	if len(fields) == 0 {
		return nil, nil
	}
	return map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{patchContainer},
				},
			},
		},
	}, fields
}

// needsProbeUpgrade returns true when we should overwrite the probe
// with our tolerant floor. Skips nil probes (deliberate disable) and
// already-tolerant probes.
func needsProbeUpgrade(p *corev1.Probe) bool {
	if p == nil {
		return false
	}
	if p.FailureThreshold > 0 && p.FailureThreshold < floorFailureThreshold {
		return true
	}
	if p.PeriodSeconds > 0 && p.PeriodSeconds < floorPeriodSeconds {
		return true
	}
	return false
}

func probePatch(existing *corev1.Probe, t Target, isReadiness bool) map[string]any {
	path := t.ProbePath
	port := t.ProbePort
	if existing != nil && existing.HTTPGet != nil {
		if existing.HTTPGet.Path != "" {
			path = existing.HTTPGet.Path
		}
		if existing.HTTPGet.Port.String() != "" {
			port = existing.HTTPGet.Port.String()
		}
	}
	period := floorPeriodSeconds
	initial := floorInitialDelay
	if isReadiness {
		period = floorReadinessPeriod
		initial = 5
	}
	return map[string]any{
		"httpGet": map[string]any{
			"path": path,
			"port": parsePort(port),
		},
		"initialDelaySeconds": initial,
		"periodSeconds":       period,
		"timeoutSeconds":      floorTimeoutSeconds,
		"failureThreshold":    floorFailureThreshold,
	}
}

// parsePort returns an int (port number) when the port string parses
// as a number, else returns the string (named port). The kube API
// accepts both shapes in JSON.
func parsePort(p string) any {
	var n int
	if _, err := fmt.Sscanf(p, "%d", &n); err == nil && n > 0 && n <= 65535 {
		return n
	}
	return p
}

func needRaise(have corev1.ResourceList, k corev1.ResourceName, floor string) bool {
	if floor == "" {
		return false
	}
	cur, has := have[k]
	if !has || cur.IsZero() {
		return true
	}
	fq, err := resource.ParseQuantity(floor)
	if err != nil {
		return false
	}
	return cur.Cmp(fq) < 0
}

func ensure(m map[string]map[string]string, k string) map[string]string {
	if v, ok := m[k]; ok {
		return v
	}
	m[k] = map[string]string{}
	return m[k]
}

func findContainer(dep *appsv1.Deployment, name string) *corev1.Container {
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == name {
			return &dep.Spec.Template.Spec.Containers[i]
		}
	}
	return nil
}
