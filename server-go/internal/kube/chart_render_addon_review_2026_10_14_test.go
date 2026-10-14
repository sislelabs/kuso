package kube

import (
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var retentionEnv = regexp.MustCompile(`name: RETENTION_DAYS, value: "([^"]*)"`)

// retentionDays 0 is "keep forever"; `| default 14` used to turn it back
// into a 14-day prune. Absent must still mean 14.
func TestAddonBackupChart_RetentionZeroKeepsForever(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind string
		sets []string
		want string
	}{
		{"postgres", []string{"backup.retentionDays=0"}, "0"},
		{"mysql", []string{"backup.retentionDays=0"}, "0"},
		{"redis", []string{"backup.retentionDays=0"}, "0"},
		{"postgres", []string{"backup.retentionDays=30"}, "30"},
		{"postgres", []string{"backup.retentionDays=null"}, "14"},
		{"postgres", nil, "14"},
	}
	for _, tc := range cases {
		sets := append([]string{"backup.schedule=0 3 * * *"}, tc.sets...)
		out := helmTemplateAddon(t, tc.kind, sets...)
		m := retentionEnv.FindAllStringSubmatch(out, -1)
		if len(m) == 0 {
			t.Fatalf("%s %v: no RETENTION_DAYS rendered", tc.kind, tc.sets)
		}
		for _, g := range m {
			if g[1] != tc.want {
				t.Errorf("%s %v: RETENTION_DAYS=%q, want %q", tc.kind, tc.sets, g[1], tc.want)
			}
		}
	}
}

// redpanda has no auth and auto-creates topics; it must never get a public
// TCP route even if publicTCP was enabled on the CR before the API refused it.
func TestAddonChart_RedpandaNeverPublicTCP(t *testing.T) {
	t.Parallel()
	out := helmTemplateAddon(t, "redpanda", "publicTCP.enabled=true", "publicTCP.port=30001")
	if strings.Contains(out, "kind: IngressRouteTCP") {
		t.Fatalf("redpanda rendered a public TCP route:\n%s", routeSection(out))
	}
}

// CNPG copies only inheritedMetadata (Cluster) and template.metadata
// (Pooler) onto the pods it creates. Without the project label the HA DB
// pods sit outside the project NetworkPolicies.
func TestAddonChart_CNPGPodsCarryProjectLabels(t *testing.T) {
	t.Parallel()
	out := helmTemplateAddon(t, "postgres", "ha=true", "pooler.enabled=true")
	want := map[string]string{
		"kuso.sislelabs.com/project":    "alpha",
		"kuso.sislelabs.com/addon":      "test-addon",
		"kuso.sislelabs.com/addon-kind": "postgres",
	}
	seen := map[string]bool{}
	for _, doc := range strings.Split(out, "\n---\n") {
		var obj struct {
			Kind string `yaml:"kind"`
			Spec struct {
				InheritedMetadata struct {
					Labels map[string]string `yaml:"labels"`
				} `yaml:"inheritedMetadata"`
				Template struct {
					Metadata struct {
						Labels map[string]string `yaml:"labels"`
					} `yaml:"metadata"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			continue
		}
		var got map[string]string
		switch obj.Kind {
		case "Cluster":
			got = obj.Spec.InheritedMetadata.Labels
		case "Pooler":
			got = obj.Spec.Template.Metadata.Labels
		default:
			continue
		}
		seen[obj.Kind] = true
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s pod labels: %s=%q, want %q (all: %v)", obj.Kind, k, got[k], v, got)
			}
		}
		if _, ok := got["app.kubernetes.io/instance"]; ok {
			t.Errorf("%s pod labels must not carry the release selector labels: %v", obj.Kind, got)
		}
	}
	if !seen["Cluster"] || !seen["Pooler"] {
		t.Fatalf("expected a Cluster and a Pooler, saw %v", seen)
	}
}
