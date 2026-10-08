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
//
// Limits are raised when one is already set, never introduced. This
// package used to stamp 256Mi / 500m on a Traefik that shipped with no
// limits; on 2026-10-08 a load test against one app OOM-killed both
// ingress replicas at that limit and took every host down with them.
package platformharden

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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
	// always tune up by hand and we won't overwrite. A limit floor
	// applies only to a container that already has that limit.
	CPURequest, MemRequest, CPULimit, MemLimit string
	// LegacyCPULimit is a CPU limit an earlier kuso stamped on this
	// container and that should not be there at all. A limit equal to
	// it is removed; any other value was set by the operator and stays.
	LegacyCPULimit string
	// GoMemLimitPercent sets GOMEMLIMIT to this share of the container's
	// memory limit when it has one and the env is unset. For Go programs
	// only. 0 = leave alone.
	GoMemLimitPercent int
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
		// Every public route goes through this pod, so it gets real
		// guaranteed CPU and memory. Memory grows with open
		// connections (about 0.1Mi each, measured during the incident):
		// 1Gi holds several thousand per replica.
		CPURequest:     "250m",
		MemRequest:     "256Mi",
		MemLimit:       "1Gi",
		LegacyCPULimit: "500m",
		// Past this the Go collector runs harder instead of the kernel
		// OOM-killing the proxy.
		GoMemLimitPercent: 80,
		ProbePath:         "/ping",
		ProbePort:         "8080",
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
	{
		Namespace:     "kuso",
		Deployment:    "kuso-prometheus",
		ContainerName: "prometheus",
	},
}

// PriorityClass is the class deploy/server-go.yaml creates for platform
// pods, so node pressure evicts tenant workloads before them.
const PriorityClass = "kuso-platform"

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
	// Naming a class that doesn't exist makes the apiserver reject every
	// new pod of the deployment, so only set it once it is confirmed.
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	_, err := kc.Clientset.SchedulingV1().PriorityClasses().Get(pctx, PriorityClass, metav1.GetOptions{})
	cancel()
	if err != nil {
		logger.Warn("platformharden: priority class unavailable, leaving pod priorities alone", "class", PriorityClass, "err", err)
	}
	for _, t := range defaults {
		hardenOne(ctx, kc, t, err == nil, logger)
	}
}

const (
	operatorNamespace   = "kuso-operator-system"
	operatorDeployment  = "kuso-operator-controller-manager"
	operatorContainer   = "manager"
	operatorClusterRole = "kuso-operator"
	// IngressLimitsEnv on the operator turns on the kusoenvironment
	// chart's per-service ingress limits (operator/watches.yaml).
	IngressLimitsEnv = "KUSO_INGRESS_LIMITS"
)

// EnsureIngressLimits switches per-service ingress limits on for an
// install that predates them. The chart renders a Traefik Middleware per
// service, which the operator can only create with the traefik.io
// middlewares rule on its ClusterRole. The updater can't grant RBAC, so
// the switch is flipped here and only once that rule is visible: without
// it every environment's helm release would fail on the Middleware.
func EnsureIngressLimits(ctx context.Context, kc *kube.Client, logger *slog.Logger) {
	if kc == nil {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	dep, err := kc.Clientset.AppsV1().Deployments(operatorNamespace).Get(lctx, operatorDeployment, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Warn("platformharden: ingress limits: get operator", "err", err)
		}
		return
	}
	patch := ingressLimitsPatch(dep)
	if patch == nil {
		return
	}
	role, err := kc.Clientset.RbacV1().ClusterRoles().Get(lctx, operatorClusterRole, metav1.GetOptions{})
	if err != nil || !grantsMiddlewares(role.Rules) {
		logger.Warn("platformharden: per-service ingress limits are OFF: the kuso-operator ClusterRole lacks traefik.io middlewares; apply deploy/operator.yaml from this release to turn them on",
			"err", err)
		return
	}
	pb, err := json.Marshal(patch)
	if err != nil {
		return
	}
	if _, err := kc.Clientset.AppsV1().Deployments(operatorNamespace).Patch(
		lctx, operatorDeployment, types.StrategicMergePatchType, pb, metav1.PatchOptions{},
	); err != nil {
		logger.Warn("platformharden: ingress limits: patch operator", "err", err)
		return
	}
	logger.Info("platformharden: per-service ingress limits switched on", "env", IngressLimitsEnv)
}

// ingressLimitsPatch returns the patch that sets IngressLimitsEnv on the
// operator, or nil when the env is already there with any value: an
// explicit "false" is an operator's decision.
func ingressLimitsPatch(dep *appsv1.Deployment) map[string]any {
	c := findContainer(dep, operatorContainer)
	if c == nil {
		return nil
	}
	for _, e := range c.Env {
		if e.Name == IngressLimitsEnv {
			return nil
		}
	}
	return map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{
			"name": operatorContainer,
			"env":  []any{map[string]any{"name": IngressLimitsEnv, "value": "true"}},
		}},
	}}}}
}

// grantsMiddlewares reports whether the rules allow full management of
// traefik.io Middlewares, the verbs a helm release needs.
func grantsMiddlewares(rules []rbacv1.PolicyRule) bool {
	need := map[string]bool{"get": false, "create": false, "update": false, "patch": false, "delete": false}
	has := func(list []string, want string) bool {
		for _, v := range list {
			if v == want || v == "*" {
				return true
			}
		}
		return false
	}
	for _, r := range rules {
		if !has(r.APIGroups, "traefik.io") || !has(r.Resources, "middlewares") {
			continue
		}
		for verb := range need {
			if has(r.Verbs, verb) {
				need[verb] = true
			}
		}
	}
	for _, ok := range need {
		if !ok {
			return false
		}
	}
	return true
}

func hardenOne(ctx context.Context, kc *kube.Client, t Target, priority bool, logger *slog.Logger) {
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
	patch, fields := buildPatch(dep, t, priority)
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
// changing. priority says the kuso-platform PriorityClass exists.
func buildPatch(dep *appsv1.Deployment, t Target, priority bool) (map[string]any, []string) {
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
	resPatch := map[string]map[string]any{}
	if needRaise(c.Resources.Requests, corev1.ResourceCPU, t.CPURequest, true) {
		ensure(resPatch, "requests")["cpu"] = t.CPURequest
	}
	if needRaise(c.Resources.Requests, corev1.ResourceMemory, t.MemRequest, true) {
		ensure(resPatch, "requests")["memory"] = t.MemRequest
	}
	if isLegacyLimit(c.Resources.Limits, corev1.ResourceCPU, t.LegacyCPULimit) {
		// null deletes the key in a strategic merge patch.
		ensure(resPatch, "limits")["cpu"] = nil
	} else if needRaise(c.Resources.Limits, corev1.ResourceCPU, t.CPULimit, false) {
		ensure(resPatch, "limits")["cpu"] = t.CPULimit
	}
	if needRaise(c.Resources.Limits, corev1.ResourceMemory, t.MemLimit, false) {
		ensure(resPatch, "limits")["memory"] = t.MemLimit
	}
	if len(resPatch) > 0 {
		patchContainer["resources"] = resPatch
		fields = append(fields, "resources")
	}

	if v := goMemLimit(c, t); v != "" {
		patchContainer["env"] = []any{map[string]any{"name": "GOMEMLIMIT", "value": v}}
		fields = append(fields, "GOMEMLIMIT")
	}

	podSpec := map[string]any{}
	if len(fields) > 0 {
		podSpec["containers"] = []any{patchContainer}
	}
	if priority && dep.Spec.Template.Spec.PriorityClassName == "" {
		podSpec["priorityClassName"] = PriorityClass
		fields = append(fields, "priorityClassName")
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": podSpec,
			},
		},
	}, fields
}

// goMemLimit returns the GOMEMLIMIT to set, or "" for none. The base is
// the memory limit the container ends up with: its own, or the floor
// when that is higher.
func goMemLimit(c *corev1.Container, t Target) string {
	if t.GoMemLimitPercent <= 0 {
		return ""
	}
	for _, e := range c.Env {
		if e.Name == "GOMEMLIMIT" {
			return ""
		}
	}
	limit, has := c.Resources.Limits[corev1.ResourceMemory]
	if !has || limit.IsZero() {
		return ""
	}
	if floor, err := resource.ParseQuantity(t.MemLimit); err == nil && limit.Cmp(floor) < 0 {
		limit = floor
	}
	return fmt.Sprintf("%dMiB", limit.Value()*int64(t.GoMemLimitPercent)/100/(1<<20))
}

func isLegacyLimit(have corev1.ResourceList, k corev1.ResourceName, legacy string) bool {
	if legacy == "" {
		return false
	}
	cur, has := have[k]
	lq, err := resource.ParseQuantity(legacy)
	return has && err == nil && cur.Cmp(lq) == 0
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

// needRaise reports whether the current value is below floor. A missing
// value counts as below only when addIfUnset is true: that is right for
// requests and wrong for limits, where unset means unlimited.
func needRaise(have corev1.ResourceList, k corev1.ResourceName, floor string, addIfUnset bool) bool {
	if floor == "" {
		return false
	}
	cur, has := have[k]
	if !has || cur.IsZero() {
		return addIfUnset
	}
	fq, err := resource.ParseQuantity(floor)
	if err != nil {
		return false
	}
	return cur.Cmp(fq) < 0
}

func ensure(m map[string]map[string]any, k string) map[string]any {
	if v, ok := m[k]; ok {
		return v
	}
	m[k] = map[string]any{}
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
