package scaledown

import (
	"testing"

	"kuso/server/internal/kube"
)

func TestSleepEligible(t *testing.T) {
	t.Parallel()

	mk := func(enabled bool, exclude []string) *kube.KusoService {
		s := &kube.KusoService{}
		if enabled || exclude != nil {
			s.Spec.Sleep = &kube.KusoServiceSleep{Enabled: enabled}
			if exclude != nil {
				s.Spec.Sleep.WakeOn = &kube.KusoServiceWake{ExcludePaths: exclude}
			}
		}
		return s
	}

	cases := []struct {
		name string
		svc  *kube.KusoService
		want bool
	}{
		{"sleep off", mk(false, nil), false},
		{"sleep on, no excludes", mk(true, nil), true},
		{"sleep on, has excludes → keep warm", mk(true, []string{"/webhook"}), false},
		{"nil sleep", &kube.KusoService{}, false},
	}
	for _, c := range cases {
		if got := sleepEligible(c.svc); got != c.want {
			t.Errorf("%s: sleepEligible = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHpaManaged(t *testing.T) {
	t.Parallel()

	mk := func(scale *kube.KusoScaleSpec) *kube.KusoService {
		s := &kube.KusoService{}
		s.Spec.Scale = scale
		return s
	}
	ptr := func(i int) *int { return &i }

	cases := []struct {
		name string
		svc  *kube.KusoService
		want bool
	}{
		{"nil scale → not HPA-managed", mk(nil), false},
		// The dead-code bug: an autoscaling service (max > min) used to be
		// detected as NOT hpa-managed, so scaledown scaled it to 0 anyway.
		{"autoscaling (max > min) → hpa-managed", mk(&kube.KusoScaleSpec{Min: ptr(1), Max: 5}), true},
		{"autoscaling with implicit min=1 → hpa-managed", mk(&kube.KusoScaleSpec{Max: 3}), true},
		{"no headroom (max == min) → not hpa-managed", mk(&kube.KusoScaleSpec{Min: ptr(2), Max: 2}), false},
		{"max unset (0) with min=1 → not hpa-managed", mk(&kube.KusoScaleSpec{Min: ptr(1)}), false},
		{"scale-to-zero min=0, no max → not hpa-managed", mk(&kube.KusoScaleSpec{Min: ptr(0)}), false},
	}
	for _, c := range cases {
		if got := hpaManaged(c.svc); got != c.want {
			t.Errorf("%s: hpaManaged = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEscapePromLabel(t *testing.T) {
	t.Parallel()
	// dots and dashes appear in real ns-env names; dots must be escaped
	// (regex any-char) but dashes are literal in PromQL =~.
	got := escapePromLabel("kuso-papelito-web-production")
	want := "kuso-papelito-web-production"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := escapePromLabel("a.b"); got != `a\.b` {
		t.Errorf("dot escape: got %q", got)
	}
}
