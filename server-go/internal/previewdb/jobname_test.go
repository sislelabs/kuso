package previewdb

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// DATA-2 / DATA-7: the nonce sat at the end of the Job name and was cut by
// the 63-char truncation, so every later migrate/seed for a long-named
// clone collided with the previous Job and was "observed" as done.
func TestJobNames_KeepNonceWhenTruncated(t *testing.T) {
	const day = 86400
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "scubatony-backend-pr-123"},
		Spec: kube.KusoEnvironmentSpec{
			Image:   &kube.KusoImage{Repository: "r", Tag: "t"},
			Release: &kube.KusoReleaseSpec{Command: []string{"migrate"}},
		},
	}
	cases := map[string][2]string{
		"migrate": {
			buildMigrateJob("kuso", "scubatony", "scubatony-postgres-pr-123", env, "", 1759000000).Name,
			buildMigrateJob("kuso", "scubatony", "scubatony-postgres-pr-123", env, "", 1759000000+day).Name,
		},
		"seed": {
			buildSeedJob("kuso", "design-system", "design-system-postgres-main", "design-system-postgres-main-pr-1234", "", 1759000000).Name,
			buildSeedJob("kuso", "design-system", "design-system-postgres-main", "design-system-postgres-main-pr-1234", "", 1759000000+day).Name,
		},
	}
	for kind, names := range cases {
		a, b := names[0], names[1]
		if a == b {
			t.Errorf("%s: two runs a day apart share Job name %q", kind, a)
		}
		for _, n := range names {
			if len(n) > 63 {
				t.Errorf("%s: name %q is %d chars, over the 63 limit", kind, n, len(n))
			}
			if strings.HasSuffix(n, "-") || strings.Contains(n, "--") {
				t.Errorf("%s: name %q is not a valid DNS label", kind, n)
			}
		}
	}
}

func TestUniqueJobName_ShortNameUnchanged(t *testing.T) {
	if got := uniqueJobName("alpha-pg-pr-9-seed-from-pg", 1780000000); got != "alpha-pg-pr-9-seed-from-pg-1780000000" {
		t.Errorf("short name rewritten: %q", got)
	}
}

func TestUniqueJobName_DistinctPrefixesSharingTruncation(t *testing.T) {
	long := strings.Repeat("a", 70)
	if uniqueJobName(long+"-x", 1) == uniqueJobName(long+"-y", 1) {
		t.Error("prefixes that differ only past the cut collide")
	}
}
