package marketplace

import (
	"errors"
	"strings"
	"testing"

	"kuso/server/internal/projects"
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
		rendered, _, err := RenderTemplate(m, e.TemplateYAML, "smoke", answers)
		if err != nil {
			t.Errorf("%s: render with stub answers failed: %v", m.Name, err)
			continue
		}
		// Every addon/service env-var reference must be a whole-string
		// ref: ParseVarRef rejects composites like "${{ a.HOST }}:${{ a.PORT }}"
		// at apply time (ErrCompositeVarRef), silently dropping the env
		// var. Prompt refs are substituted before this point into plain
		// strings, so they're exempt (and correctly not rejected here).
		for _, s := range rendered.Services {
			for k, ev := range s.Env {
				if ev.Generate != "" {
					continue // generated secrets are literals, never refs
				}
				if _, _, perr := projects.ParseVarRef(ev.Value); perr != nil {
					t.Errorf("%s: env %s=%q is not a valid whole-string ref: %v", m.Name, k, ev.Value, perr)
				}
			}
		}
	}
}

func TestGetEntry_NotFound(t *testing.T) {
	if _, err := GetEntry("does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
