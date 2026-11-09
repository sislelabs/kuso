package kusoCli

import (
	"testing"

	"kuso/pkg/kusoApi"
)

func TestSplitImageRef(t *testing.T) {
	cases := []struct{ in, repo, tag string }{
		{"nginx", "nginx", ""},
		{"nginx:1.27", "nginx", "1.27"},
		{"localhost:5000/app", "localhost:5000/app", ""},
		{"localhost:5000/app:v2", "localhost:5000/app", "v2"},
		{"ghcr.io/acme/api:sha-1", "ghcr.io/acme/api", "sha-1"},
	}
	for _, c := range cases {
		repo, tag := splitImageRef(c.in)
		if repo != c.repo || tag != c.tag {
			t.Errorf("splitImageRef(%q) = %q, %q; want %q, %q", c.in, repo, tag, c.repo, c.tag)
		}
	}
}

func TestHealthcheckPatchKeepsUnchangedFields(t *testing.T) {
	cur := &currentServiceSpec{}
	cur.Spec.Healthcheck = &kusoApi.Healthcheck{Path: "/old", PeriodSeconds: 15, TimeoutSeconds: 3}
	current := func() (*currentServiceSpec, error) { return cur, nil }
	changedSet := func(names ...string) func(string) bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return func(n string) bool { return m[n] }
	}

	serviceSetHCPeriod = 5
	hc, err := healthcheckPatch(changedSet("healthcheck-period"), current)
	if err != nil || hc == nil || hc.Path != "/old" || hc.PeriodSeconds != 5 || hc.TimeoutSeconds != 3 {
		t.Fatalf("tuning only: got %+v, %v", hc, err)
	}

	serviceSetHCPath = ""
	hc, err = healthcheckPatch(changedSet("healthcheck-path"), current)
	if err != nil || hc == nil || *hc != (kusoApi.Healthcheck{}) {
		t.Fatalf("clear: got %+v, %v", hc, err)
	}

	if hc, err := healthcheckPatch(changedSet(), current); hc != nil || err != nil {
		t.Fatalf("no flags: got %+v, %v", hc, err)
	}

	cur.Spec.Healthcheck = nil
	if _, err := healthcheckPatch(changedSet("healthcheck-period"), current); err == nil {
		t.Fatal("tuning a missing health check without --healthcheck-path should fail")
	}
}
