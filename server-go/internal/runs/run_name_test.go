package runs

import (
	"strings"
	"testing"

	"kuso/server/internal/kube"
)

// BLD-19: a 42-char FQN made a 55-char name that helm refuses to install.
func TestGenRunName_FitsHelmReleaseName(t *testing.T) {
	t.Parallel()
	cases := []struct{ project, service string }{
		{"alpha", "web"},
		{"projectname-abcdefgh", "service-name-abcdefghi"}, // 42-char FQN
		{strings.Repeat("p", 40), strings.Repeat("s", 60)},
	}
	for _, c := range cases {
		name := genRunName(c.project, c.service)
		if err := kube.ValidateReleaseName(name); err != nil {
			t.Errorf("genRunName(%q, %q) = %q: %v", c.project, c.service, name, err)
		}
		if !runNameRE.MatchString(name) {
			t.Errorf("genRunName(%q, %q) = %q fails runNameRE", c.project, c.service, name)
		}
	}
	if got := genRunName("alpha", "web"); !strings.HasPrefix(got, "alpha-web-run-") {
		t.Errorf("short name was rewritten: %q", got)
	}
	a := genRunName("projectname-abcdefgh", "service-name-abcdefghi")
	b := genRunName("projectname-abcdefgh", "service-name-abcdefghj")
	if strings.TrimSuffix(a, a[strings.LastIndex(a, "-run-"):]) == strings.TrimSuffix(b, b[strings.LastIndex(b, "-run-"):]) {
		t.Errorf("distinct long services share a prefix: %q vs %q", a, b)
	}
}
