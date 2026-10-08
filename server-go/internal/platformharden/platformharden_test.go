package platformharden

import (
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func deployment(prio string, res corev1.ResourceRequirements) *appsv1.Deployment {
	d := &appsv1.Deployment{}
	d.Spec.Template.Spec.PriorityClassName = prio
	d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "traefik", Resources: res}}
	return d
}

func quantities(kv ...string) corev1.ResourceList {
	out := corev1.ResourceList{}
	for i := 0; i < len(kv); i += 2 {
		out[corev1.ResourceName(kv[i])] = resource.MustParse(kv[i+1])
	}
	return out
}

func traefikTarget(t *testing.T) Target {
	t.Helper()
	for _, tg := range defaults {
		if tg.Deployment == "traefik" {
			return tg
		}
	}
	t.Fatal("no traefik target")
	return Target{}
}

// patched returns the container and pod-spec halves of the patch as
// generic JSON, the shape the apiserver receives.
func patched(t *testing.T, dep *appsv1.Deployment, tg Target, priority bool) (container, podSpec map[string]any) {
	t.Helper()
	patch, _ := buildPatch(dep, tg, priority)
	if patch == nil {
		return nil, nil
	}
	raw, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Spec struct {
			Template struct {
				Spec map[string]any `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	podSpec = out.Spec.Template.Spec
	if cs, ok := podSpec["containers"].([]any); ok && len(cs) == 1 {
		container, _ = cs[0].(map[string]any)
	}
	return container, podSpec
}

// 2026-10-08: the ingress controller ran with no limits until this
// package stamped 256Mi / 500m on it. A load test on one app then
// OOM-killed both replicas and every host went down. A container with
// no limit must be left without one.
func TestBuildPatch_NeverAddsALimitToAnUnlimitedContainer(t *testing.T) {
	for _, tg := range defaults {
		dep := deployment("kuso-platform", corev1.ResourceRequirements{})
		dep.Spec.Template.Spec.Containers[0].Name = tg.ContainerName
		c, _ := patched(t, dep, tg, false)
		res, _ := c["resources"].(map[string]any)
		if _, has := res["limits"]; has {
			t.Errorf("%s: limits added to an unlimited container: %v", tg.Deployment, res["limits"])
		}
		if _, has := res["requests"]; !has && tg.CPURequest != "" {
			t.Errorf("%s: requests not raised: %v", tg.Deployment, res)
		}
	}
}

// Clusters hardened by an earlier kuso carry the stamped limits. The
// memory limit is raised and the CPU limit kuso added is removed: at
// 500m the proxy was throttled from 1,600 req/s up.
func TestBuildPatch_TraefikLegacyLimitsAreLifted(t *testing.T) {
	dep := deployment("", corev1.ResourceRequirements{
		Requests: quantities("cpu", "100m", "memory", "96Mi"),
		Limits:   quantities("cpu", "500m", "memory", "256Mi"),
	})
	c, _ := patched(t, dep, traefikTarget(t), false)
	res := c["resources"].(map[string]any)
	limits := res["limits"].(map[string]any)
	if v, has := limits["cpu"]; !has || v != nil {
		t.Errorf("stamped cpu limit must be removed (null), got %v present=%v", v, has)
	}
	if limits["memory"] != "1Gi" {
		t.Errorf("memory limit = %v, want 1Gi", limits["memory"])
	}
	requests := res["requests"].(map[string]any)
	if requests["cpu"] != "250m" || requests["memory"] != "256Mi" {
		t.Errorf("requests = %v, want 250m / 256Mi", requests)
	}
}

func TestBuildPatch_KeepsOperatorTunedValues(t *testing.T) {
	dep := deployment("system-cluster-critical", corev1.ResourceRequirements{
		Requests: quantities("cpu", "1", "memory", "1Gi"),
		Limits:   quantities("cpu", "2", "memory", "4Gi"),
	})
	dep.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "GOMEMLIMIT", Value: "3GiB"}}
	if patch, fields := buildPatch(dep, traefikTarget(t), true); patch != nil {
		t.Fatalf("nothing is below the floor, got patch for %v: %v", fields, patch)
	}
}

func TestBuildPatch_PriorityClassOnlyWhenItExistsAndIsUnset(t *testing.T) {
	res := corev1.ResourceRequirements{Requests: quantities("cpu", "1", "memory", "1Gi")}
	if _, spec := patched(t, deployment("", res), traefikTarget(t), true); spec["priorityClassName"] != PriorityClass {
		t.Errorf("priorityClassName = %v, want %s", spec["priorityClassName"], PriorityClass)
	}
	// Naming a class the cluster doesn't have makes the apiserver reject
	// every new pod of the deployment.
	if patch, _ := buildPatch(deployment("", res), traefikTarget(t), false); patch != nil {
		t.Errorf("priority class must not be set when it doesn't exist: %v", patch)
	}
}

func TestGrantsMiddlewares(t *testing.T) {
	all := []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	cases := map[string]struct {
		rules []rbacv1.PolicyRule
		want  bool
	}{
		"no traefik rule":       {[]rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: all}}, false},
		"only ingressroutetcps": {[]rbacv1.PolicyRule{{APIGroups: []string{"traefik.io"}, Resources: []string{"ingressroutetcps"}, Verbs: all}}, false},
		"read-only middlewares": {[]rbacv1.PolicyRule{{APIGroups: []string{"traefik.io"}, Resources: []string{"middlewares"}, Verbs: []string{"get", "list", "watch"}}}, false},
		"explicit":              {[]rbacv1.PolicyRule{{APIGroups: []string{"traefik.io"}, Resources: []string{"ingressroutetcps", "middlewares"}, Verbs: all}}, true},
		"wildcards":             {[]rbacv1.PolicyRule{{APIGroups: []string{"traefik.io"}, Resources: []string{"*"}, Verbs: []string{"*"}}}, true},
		"verbs split over two rules": {[]rbacv1.PolicyRule{
			{APIGroups: []string{"traefik.io"}, Resources: []string{"middlewares"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"traefik.io"}, Resources: []string{"middlewares"}, Verbs: []string{"create", "update", "patch", "delete"}},
		}, true},
	}
	for name, tc := range cases {
		if got := grantsMiddlewares(tc.rules); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestIngressLimitsPatch(t *testing.T) {
	dep := &appsv1.Deployment{}
	dep.Spec.Template.Spec.Containers = []corev1.Container{{Name: "manager"}}
	if ingressLimitsPatch(dep) == nil {
		t.Fatal("operator without the env must be patched")
	}
	dep.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: IngressLimitsEnv, Value: "true"}}
	if p := ingressLimitsPatch(dep); p != nil {
		t.Fatalf("already set, got patch %v", p)
	}
	// An operator who turned it off keeps it off.
	dep.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: IngressLimitsEnv, Value: "false"}}
	if p := ingressLimitsPatch(dep); p != nil {
		t.Fatalf("explicit false must be left alone, got %v", p)
	}
}

// Traefik is a Go program: with GOMEMLIMIT just under the cgroup limit
// the collector works harder as memory fills up, instead of the kernel
// killing the pod. In the sandbox a 1Gi replica was OOM-killed at 12,000
// connections without it and survived 20,000 with it.
func TestBuildPatch_GoMemLimitFollowsTheMemoryLimit(t *testing.T) {
	tg := traefikTarget(t)
	env := func(dep *appsv1.Deployment) any {
		c, _ := patched(t, dep, tg, false)
		return c["env"]
	}
	// Legacy 256Mi limit is raised to 1Gi; GOMEMLIMIT is 80% of that.
	legacy := deployment("", corev1.ResourceRequirements{Limits: quantities("cpu", "500m", "memory", "256Mi")})
	got, _ := json.Marshal(env(legacy))
	if string(got) != `[{"name":"GOMEMLIMIT","value":"819MiB"}]` {
		t.Errorf("env = %s", got)
	}
	// An operator's larger limit is the base.
	big := deployment("", corev1.ResourceRequirements{Limits: quantities("memory", "2Gi")})
	got, _ = json.Marshal(env(big))
	if string(got) != `[{"name":"GOMEMLIMIT","value":"1638MiB"}]` {
		t.Errorf("env = %s", got)
	}
	// No limit, nothing to stay under.
	if e := env(deployment("", corev1.ResourceRequirements{})); e != nil {
		t.Errorf("unlimited container got GOMEMLIMIT: %v", e)
	}
	// Already set: left alone.
	set := deployment("", corev1.ResourceRequirements{Limits: quantities("memory", "2Gi")})
	set.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "GOMEMLIMIT", Value: "1GiB"}}
	if e := env(set); e != nil {
		t.Errorf("existing GOMEMLIMIT overwritten: %v", e)
	}
}
