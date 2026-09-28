package scaledown

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func TestIsProductionEnv(t *testing.T) {
	t.Parallel()

	svc := func(envLabel string) *kube.KusoService {
		s := &kube.KusoService{ObjectMeta: metav1.ObjectMeta{Name: "alpha-web"}}
		if envLabel != "" {
			s.Labels = map[string]string{kube.LabelEnv: envLabel}
		}
		return s
	}
	env := func(kind, envLabel string) *kube.KusoEnvironment {
		e := &kube.KusoEnvironment{}
		e.Spec.Kind = kind
		if envLabel != "" {
			e.Labels = map[string]string{kube.LabelEnv: envLabel}
		}
		return e
	}

	cases := []struct {
		name string
		svc  *kube.KusoService
		env  *kube.KusoEnvironment
		want bool
	}{
		{"production env of a production service", svc(""), env("production", "production"), true},
		{"legacy env without kind or label", svc(""), env("", ""), true},
		{"service labelled env=production", svc("production"), env("production", "production"), true},
		{"named env (staging)", svc(""), env("custom", "staging"), false},
		{"PR preview", svc(""), env("preview", ""), false},
		// Env-group clones are separate services whose env CR still says
		// kind=production; the group label is what marks them.
		{"env-group clone env", svc("verify"), env("production", "verify"), false},
		{"clone env label only", svc(""), env("production", "verify"), false},
		{"clone service, env CR without a group label", svc("verify"), env("production", ""), false},
	}
	for _, c := range cases {
		if got := isProductionEnv(c.svc, c.env); got != c.want {
			t.Errorf("%s: isProductionEnv = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestResolvePolicy(t *testing.T) {
	t.Parallel()

	type opt func(*kube.KusoService, *kube.KusoEnvironment, *kube.KusoProject)
	prod := func(_ *kube.KusoService, e *kube.KusoEnvironment, _ *kube.KusoProject) {
		e.Spec.Kind = "production"
	}
	preview := func(_ *kube.KusoService, e *kube.KusoEnvironment, _ *kube.KusoProject) {
		e.Spec.Kind = "preview"
	}
	staging := func(_ *kube.KusoService, e *kube.KusoEnvironment, _ *kube.KusoProject) {
		e.Spec.Kind = "custom"
		e.Labels = map[string]string{kube.LabelEnv: "staging"}
	}
	sleepOn := func(after int) opt {
		return func(s *kube.KusoService, _ *kube.KusoEnvironment, _ *kube.KusoProject) {
			if s.Spec.Sleep == nil {
				s.Spec.Sleep = &kube.KusoServiceSleep{}
			}
			s.Spec.Sleep.Enabled = true
			s.Spec.Sleep.AfterMinutes = after
		}
	}
	nonProdOff := func(s *kube.KusoService, _ *kube.KusoEnvironment, _ *kube.KusoProject) {
		if s.Spec.Sleep == nil {
			s.Spec.Sleep = &kube.KusoServiceSleep{}
		}
		s.Spec.Sleep.NonProduction = kube.SleepNonProductionOff
	}
	excludes := func(s *kube.KusoService, _ *kube.KusoEnvironment, _ *kube.KusoProject) {
		if s.Spec.Sleep == nil {
			s.Spec.Sleep = &kube.KusoServiceSleep{}
		}
		s.Spec.Sleep.WakeOn = &kube.KusoServiceWake{ExcludePaths: []string{"/webhook"}}
	}
	stopped := func(s *kube.KusoService, _ *kube.KusoEnvironment, _ *kube.KusoProject) {
		s.Spec.Stopped = true
	}
	envStopped := func(_ *kube.KusoService, e *kube.KusoEnvironment, _ *kube.KusoProject) {
		e.Spec.Stopped = true
	}
	worker := func(_ *kube.KusoService, e *kube.KusoEnvironment, _ *kube.KusoProject) {
		e.Spec.Runtime = "worker"
	}
	internal := func(_ *kube.KusoService, e *kube.KusoEnvironment, _ *kube.KusoProject) {
		e.Spec.Internal = true
	}
	alwaysOn := func(_ *kube.KusoService, _ *kube.KusoEnvironment, p *kube.KusoProject) {
		p.Spec.AlwaysOn = true
	}
	hpa := func(s *kube.KusoService, _ *kube.KusoEnvironment, _ *kube.KusoProject) {
		min := 1
		s.Spec.Scale = &kube.KusoScaleSpec{Min: &min, Max: 5}
	}

	cases := []struct {
		name          string
		opts          []opt
		wantProd      bool
		wantAllowed   bool
		wantEligible  bool
		wantAutoRoute bool
		wantAfter     int
	}{
		{"production, sleep off → never sleeps", []opt{prod}, true, false, false, false, 30},
		{"production, opted in", []opt{prod, sleepOn(15)}, true, true, true, false, 15},
		{"production, opted in with default window", []opt{prod, sleepOn(0)}, true, true, true, false, 30},
		{"production opted in, nonProduction off does not matter", []opt{prod, sleepOn(10), nonProdOff}, true, true, true, false, 10},
		{"preview sleeps by default", []opt{preview}, false, true, true, true, 30},
		{"named env sleeps by default", []opt{staging}, false, true, true, true, 30},
		{"named env reuses the service window", []opt{staging, sleepOn(12)}, false, true, true, true, 12},
		{"named env, service opted out", []opt{staging, nonProdOff}, false, false, false, false, 30},
		{"preview, service opted out even with prod sleep on", []opt{preview, sleepOn(5), nonProdOff}, false, false, false, false, 5},
		{"excludePaths keeps non-prod warm", []opt{staging, excludes}, false, false, false, false, 30},
		{"excludePaths keeps prod warm", []opt{prod, sleepOn(10), excludes}, true, false, false, false, 10},
		// Stopped blocks the scale action but not the routing: a stop/start
		// toggle must not flap the env's Ingress.
		{"stopped service: allowed but not eligible", []opt{staging, stopped}, false, true, false, true, 30},
		{"stopped env: allowed but not eligible", []opt{staging, envStopped}, false, true, false, true, 30},
		// No Ingress → no activator in path → nothing could ever wake it.
		{"worker never sleeps", []opt{staging, worker}, false, false, false, false, 30},
		{"internal never sleeps", []opt{staging, internal}, false, false, false, false, 30},
		{"project alwaysOn wins over opt-in", []opt{prod, sleepOn(10), alwaysOn}, true, false, false, false, 10},
		{"project alwaysOn wins over non-prod default", []opt{preview, alwaysOn}, false, false, false, false, 30},
		// B: autoscaling no longer blocks sleep.
		{"HPA-managed production env can sleep", []opt{prod, sleepOn(10), hpa}, true, true, true, false, 10},
		{"HPA-managed preview sleeps by default", []opt{preview, hpa}, false, true, true, true, 30},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			svc := &kube.KusoService{ObjectMeta: metav1.ObjectMeta{Name: "alpha-web"}}
			env := &kube.KusoEnvironment{}
			proj := &kube.KusoProject{}
			for _, o := range c.opts {
				o(svc, env, proj)
			}
			got := resolvePolicy(proj, svc, env)
			if got.Production != c.wantProd {
				t.Errorf("Production = %v, want %v", got.Production, c.wantProd)
			}
			if got.Allowed != c.wantAllowed {
				t.Errorf("Allowed = %v, want %v", got.Allowed, c.wantAllowed)
			}
			if got.Eligible() != c.wantEligible {
				t.Errorf("Eligible = %v, want %v", got.Eligible(), c.wantEligible)
			}
			if got.AutoRoute != c.wantAutoRoute {
				t.Errorf("AutoRoute = %v, want %v", got.AutoRoute, c.wantAutoRoute)
			}
			if got.AfterMinutes != c.wantAfter {
				t.Errorf("AfterMinutes = %d, want %d", got.AfterMinutes, c.wantAfter)
			}
		})
	}

	t.Run("nil project is not alwaysOn", func(t *testing.T) {
		t.Parallel()
		env := &kube.KusoEnvironment{}
		env.Spec.Kind = "preview"
		if got := resolvePolicy(nil, &kube.KusoService{}, env); !got.Eligible() {
			t.Error("preview with nil project should be eligible")
		}
	})
}

// A slept env's Ingress must point at the activator, or the next request
// hits a 0-endpoint Service and 503s with nothing to wake it.
func TestActivatorRouted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		backend string
		want    bool
	}{
		{"shared activator in kuso ns", "kuso-activator", true},
		{"per-env ExternalName mirror", "alpha-web-staging-activator", true},
		{"app's own service", "alpha-web-staging", false},
		{"no backend", "", false},
	}
	for _, c := range cases {
		if got := activatorBackend(c.backend); got != c.want {
			t.Errorf("%s: activatorBackend(%q) = %v, want %v", c.name, c.backend, got, c.want)
		}
	}
}
