package kube

import (
	"strings"
	"testing"
)

const (
	egressPublicLabel      = `kuso.sislelabs.com/network-egress-public: "true"`
	egressPlatformAPILabel = `kuso.sislelabs.com/network-egress-platform-api: "true"`
)

// podTemplateLabels returns the text of the pod template's labels block —
// the only labels the project NetworkPolicy podSelectors see. Matching on
// the whole render would also hit CronJob/Job metadata labels.
func podTemplateLabels(t *testing.T, out string) string {
	t.Helper()
	i := strings.LastIndex(out, "template:")
	if i < 0 {
		t.Fatalf("no pod template in render:\n%s", out)
	}
	rest := out[i:]
	j := strings.Index(rest, "spec:")
	if j < 0 {
		t.Fatalf("pod template has no spec:\n%s", rest)
	}
	return rest[:j]
}

// Cron and run pods sit behind the same default-deny project
// NetworkPolicy as service pods, which allows public egress only for pods
// carrying the network-egress-public label. Without it a cron's curl to
// its own public URL is REJECTed. The label must follow the owning
// service's privateEgress/platformApiEgress exactly.
func TestKusoCronChart_EgressLabels(t *testing.T) {
	t.Parallel()
	kinds := map[string][]string{
		"service": {"kind=service", "service=web", "image.repository=registry.local/alpha/web", "command[0]=run"},
		"command": {"kind=command", "image.repository=registry.local/tools", "command[0]=run"},
		"http":    {"kind=http", "url=https://example.com/api/cron"},
	}
	for kind, base := range kinds {
		kind, base := kind, base
		t.Run(kind+"_default_public", func(t *testing.T) {
			t.Parallel()
			labels := podTemplateLabels(t, helmTemplateCron(t, base...))
			if !strings.Contains(labels, egressPublicLabel) {
				t.Errorf("default render missing public-egress pod label:\n%s", labels)
			}
			if strings.Contains(labels, egressPlatformAPILabel) {
				t.Errorf("default render must not grant platform-api egress:\n%s", labels)
			}
		})
		t.Run(kind+"_private", func(t *testing.T) {
			t.Parallel()
			labels := podTemplateLabels(t, helmTemplateCron(t, append(base, "privateEgress=true")...))
			if strings.Contains(labels, egressPublicLabel) {
				t.Errorf("privateEgress=true must drop the public-egress label:\n%s", labels)
			}
		})
		t.Run(kind+"_platform_api", func(t *testing.T) {
			t.Parallel()
			labels := podTemplateLabels(t, helmTemplateCron(t, append(base, "platformApiEgress=true")...))
			if !strings.Contains(labels, egressPlatformAPILabel) {
				t.Errorf("platformApiEgress=true missing platform-api label:\n%s", labels)
			}
		})
	}
}

func TestKusoRunChart_EgressLabels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		sets         []string
		wantPublic   bool
		wantPlatform bool
	}{
		{"default_public", nil, true, false},
		{"private", []string{"privateEgress=true"}, false, false},
		{"platform_api", []string{"platformApiEgress=true"}, true, true},
		{"private_with_platform_api", []string{"privateEgress=true", "platformApiEgress=true"}, false, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			labels := podTemplateLabels(t, helmTemplateRun(t, tc.sets...))
			if got := strings.Contains(labels, egressPublicLabel); got != tc.wantPublic {
				t.Errorf("public-egress label present=%v, want %v:\n%s", got, tc.wantPublic, labels)
			}
			if got := strings.Contains(labels, egressPlatformAPILabel); got != tc.wantPlatform {
				t.Errorf("platform-api label present=%v, want %v:\n%s", got, tc.wantPlatform, labels)
			}
		})
	}
}
