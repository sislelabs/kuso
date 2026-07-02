package marketplace

import (
	"errors"
	"strings"
	"testing"
)

const tmplWithPrompt = `apiVersion: kuso/v1
project: PLACEHOLDER
services:
  - name: app
    runtime: image
    port: 3000
    image: { repository: ghcr.io/x/app, tag: "1.0" }
    domains:
      - { host: "${{ prompt.host }}", tls: true }
    env:
      ADMIN_EMAIL: "${{ prompt.admin_email }}"
      SECRET: { generate: hex32 }
`

func mustManifest(t *testing.T, prompts ...Prompt) *Manifest {
	t.Helper()
	return &Manifest{Name: "app", Title: "App", Description: "d", Category: "data", Prompts: prompts}
}

func TestRenderTemplate_Substitutes(t *testing.T) {
	m := mustManifest(t,
		Prompt{Key: "host", Title: "Host", Kind: "domain", Required: true},
		Prompt{Key: "admin_email", Title: "Email", Kind: "string", Required: true},
	)
	f, notes, err := RenderTemplate(m, []byte(tmplWithPrompt), "shop",
		map[string]string{"host": "app.example.com", "admin_email": "a@b.co"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if f.Project != "shop" {
		t.Fatalf("project not set: %q", f.Project)
	}
	if f.Services[0].Domains[0].Host != "app.example.com" {
		t.Fatalf("host not substituted: %q", f.Services[0].Domains[0].Host)
	}
	if f.Services[0].Env["ADMIN_EMAIL"].Value != "a@b.co" {
		t.Fatalf("email not substituted: %q", f.Services[0].Env["ADMIN_EMAIL"].Value)
	}
	if !f.Services[0].Env["SECRET"].IsGenerated() {
		t.Fatalf("generate directive lost")
	}
	if len(notes) == 0 {
		t.Fatalf("expected notes")
	}
}

func TestRenderTemplate_MissingRequired(t *testing.T) {
	m := mustManifest(t, Prompt{Key: "host", Title: "Host", Kind: "domain", Required: true},
		Prompt{Key: "admin_email", Title: "E", Kind: "string", Required: true})
	_, _, err := RenderTemplate(m, []byte(tmplWithPrompt), "shop",
		map[string]string{"host": "app.example.com"})
	if !errors.Is(err, ErrRender) {
		t.Fatalf("want ErrRender for missing required answer, got %v", err)
	}
}

func TestRenderTemplate_UnknownToken(t *testing.T) {
	// Template references a token with no matching prompt.
	bad := strings.Replace(tmplWithPrompt, "${{ prompt.admin_email }}", "${{ prompt.nope }}", 1)
	m := mustManifest(t, Prompt{Key: "host", Title: "H", Kind: "domain", Required: true})
	_, _, err := RenderTemplate(m, []byte(bad), "shop", map[string]string{"host": "h.example.com"})
	if !errors.Is(err, ErrRender) {
		t.Fatalf("want ErrRender for unknown token, got %v", err)
	}
}

func TestRenderTemplate_NoInjection(t *testing.T) {
	// An answer containing YAML/structure must land as a plain string,
	// never alter the parsed structure (we substitute post-parse).
	m := mustManifest(t, Prompt{Key: "host", Title: "H", Kind: "domain", Required: true},
		Prompt{Key: "admin_email", Title: "E", Kind: "string", Required: true})
	f, _, err := RenderTemplate(m, []byte(tmplWithPrompt), "shop", map[string]string{
		"host":        "h.example.com",
		"admin_email": "x\"\n    EVIL: injected",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, ok := f.Services[0].Env["EVIL"]; ok {
		t.Fatalf("injection succeeded — structure was altered")
	}
	if !strings.Contains(f.Services[0].Env["ADMIN_EMAIL"].Value, "EVIL: injected") {
		t.Fatalf("answer not stored verbatim: %q", f.Services[0].Env["ADMIN_EMAIL"].Value)
	}
}
