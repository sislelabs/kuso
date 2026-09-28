package kube

import (
	"strings"
	"testing"
)

// renderedPullSecrets returns, per pod-bearing workload in the rendered
// output, the names listed under its imagePullSecrets. Line-based rather
// than a YAML decode: some charts render duplicate label keys that the
// API server tolerates but strict decoders reject.
func renderedPullSecrets(t *testing.T, out string) [][]string {
	t.Helper()
	var found [][]string
	for _, doc := range strings.Split(out, "\n---") {
		if !strings.Contains(doc, "\nkind: Deployment") && !strings.Contains(doc, "\nkind: Job") && !strings.Contains(doc, "\nkind: CronJob") {
			continue
		}
		var names []string
		lines := strings.Split(doc, "\n")
		for i, l := range lines {
			if strings.TrimSpace(l) != "imagePullSecrets:" {
				continue
			}
			indent := len(l) - len(strings.TrimLeft(l, " "))
			for _, next := range lines[i+1:] {
				ni := len(next) - len(strings.TrimLeft(next, " "))
				trimmed := strings.TrimSpace(next)
				if trimmed == "" || ni < indent || (ni == indent && !strings.HasPrefix(trimmed, "- ")) {
					break
				}
				if n, ok := strings.CutPrefix(trimmed, "- name: "); ok {
					names = append(names, strings.Trim(n, `"`))
				}
			}
		}
		found = append(found, names)
	}
	if len(found) == 0 {
		t.Fatalf("no pod-bearing workload rendered:\n%s", out)
	}
	return found
}

func assertPullSecret(t *testing.T, chart, out, want string) {
	t.Helper()
	for _, ps := range renderedPullSecrets(t, out) {
		switch {
		case want == "" && len(ps) != 0:
			t.Errorf("%s: expected no imagePullSecrets, got %+v", chart, ps)
		case want != "" && (len(ps) != 1 || ps[0] != want):
			t.Errorf("%s: imagePullSecrets = %+v, want [%s]", chart, ps, want)
		}
	}
}

func TestCharts_RenderImagePullSecret(t *testing.T) {
	t.Parallel()
	const secret = "alpha-regcred-ghcr-io"
	cronBase := []string{"service=web", "image.repository=ghcr.io/acme/web", "image.tag=v1", "command[0]=api"}

	assertPullSecret(t, "kusoenvironment", helmTemplate(t, "alpha-web-production", "image.pullSecret="+secret), secret)
	assertPullSecret(t, "kusoenvironment", helmTemplate(t, "alpha-web-production"), "")

	assertPullSecret(t, "kusocron", helmTemplateCron(t, append(cronBase, "image.pullSecret="+secret)...), secret)
	assertPullSecret(t, "kusocron", helmTemplateCron(t, cronBase...), "")

	assertPullSecret(t, "kusorun", helmTemplateRun(t, "image.pullSecret="+secret), secret)
	assertPullSecret(t, "kusorun", helmTemplateRun(t), "")
}
