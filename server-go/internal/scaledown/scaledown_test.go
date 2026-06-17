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
