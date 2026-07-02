package marketplace

import (
	"errors"
	"strings"
	"testing"

	"kuso/server/internal/spec"
)

// TestCatalog_AllTemplatesValid is the guardrail: every embedded
// template must parse, pin its image tags, reference only declared
// prompts, declare every prompt it references, and render with all
// required answers filled by a stub.
func TestCatalog_AllTemplatesValid(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("empty catalog")
	}
	for _, m := range entries {
		e, err := GetEntry(m.Name)
		if err != nil {
			t.Fatalf("%s: GetEntry: %v", m.Name, err)
		}
		f, err := spec.Parse(e.TemplateYAML)
		if err != nil {
			t.Fatalf("%s: template does not parse: %v", m.Name, err)
		}
		// Image tags must be pinned.
		for _, s := range f.Services {
			if s.Image != nil && (s.Image.Tag == "" || s.Image.Tag == "latest") {
				t.Errorf("%s: service %s image tag not pinned (%q)", m.Name, s.Name, s.Image.Tag)
			}
		}
		// Every token the template references must be a declared prompt.
		declared := map[string]bool{}
		for _, p := range m.Prompts {
			declared[p.Key] = true
		}
		for _, tok := range tokenRe.FindAllStringSubmatch(string(e.TemplateYAML), -1) {
			if !declared[tok[1]] {
				t.Errorf("%s: template uses undeclared prompt %q", m.Name, tok[1])
			}
		}
		// Every declared prompt should be referenced (no dead prompts).
		for _, p := range m.Prompts {
			if !strings.Contains(string(e.TemplateYAML), "prompt."+p.Key) {
				t.Errorf("%s: prompt %q declared but never referenced", m.Name, p.Key)
			}
		}
		// Renders cleanly with stub answers for all required prompts.
		answers := map[string]string{}
		for _, p := range m.Prompts {
			if p.Required {
				answers[p.Key] = "stub.example.com"
			}
		}
		if _, _, err := RenderTemplate(m, e.TemplateYAML, "smoke", answers); err != nil {
			t.Errorf("%s: render with stub answers failed: %v", m.Name, err)
		}
	}
}

func TestGetEntry_NotFound(t *testing.T) {
	if _, err := GetEntry("does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
