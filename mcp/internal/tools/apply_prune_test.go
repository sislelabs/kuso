package tools

import "testing"

func TestYAMLSetsPruneTrue(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want bool
	}{
		{"top-level true", "project: shop\nprune: true\n", true},
		{"top-level yes", "prune: yes\n", true},
		{"top-level on", "prune: on\n", true},
		{"quoted true", `prune: "true"`, true},
		{"explicit false", "project: shop\nprune: false\n", false},
		{"absent", "project: shop\nservices:\n  - name: web\n", false},
		{"commented out", "project: shop\n# prune: true\n", false},
		{"trailing comment", "prune: true # danger\n", true},
		// A nested key that happens to be named prune must NOT trip the
		// gate — only the document-level scalar counts.
		{"nested prune ignored", "project: shop\nservices:\n  - name: web\n    prune: true\n", false},
		// Spellings the server's yaml.v3 decode reads as prune=true.
		{"quoted key", `"prune": true`, true},
		{"flow mapping", "{project: shop, prune: true}", true},
		{"json", `{"project":"shop","prune":true}`, true},
		{"explicit tag", "prune: !!bool true\n", true},
		{"value on next line", "prune:\n  true\n", true},
		{"anchored", "x: &t true\nprune: *t\n", true},
		// Unparseable input fails closed.
		{"malformed", "prune: [\n", true},
	}
	for _, c := range cases {
		if got := yamlSetsPruneTrue(c.yaml); got != c.want {
			t.Errorf("%s: yamlSetsPruneTrue = %v, want %v", c.name, got, c.want)
		}
	}
}
