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

func TestRenderTemplate_RejectsWithErrRender(t *testing.T) {
	cases := []struct {
		name    string
		prompts []Prompt
		tmpl    string
		answers map[string]string
	}{
		{
			name: "MissingRequired",
			prompts: []Prompt{{Key: "host", Title: "Host", Kind: "domain", Required: true},
				{Key: "admin_email", Title: "E", Kind: "string", Required: true}},
			tmpl:    tmplWithPrompt,
			answers: map[string]string{"host": "app.example.com"},
		},
		{
			// Template references a token with no matching prompt.
			name:    "UnknownToken",
			prompts: []Prompt{{Key: "host", Title: "H", Kind: "domain", Required: true}},
			tmpl:    strings.Replace(tmplWithPrompt, "${{ prompt.admin_email }}", "${{ prompt.nope }}", 1),
			answers: map[string]string{"host": "h.example.com"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mustManifest(t, tc.prompts...)
			_, _, err := RenderTemplate(m, []byte(tc.tmpl), "shop", tc.answers)
			if !errors.Is(err, ErrRender) {
				t.Fatalf("want ErrRender, got %v", err)
			}
		})
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

func TestRenderTemplate_StampsManifestSize(t *testing.T) {
	m := mustManifest(t,
		Prompt{Key: "host", Title: "Host", Kind: "domain", Required: true},
		Prompt{Key: "admin_email", Title: "Email", Kind: "string", Required: true},
	)
	m.Size = "large"
	f, _, err := RenderTemplate(m, []byte(tmplWithPrompt), "shop",
		map[string]string{"host": "app.example.com", "admin_email": "a@b.co"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Services[0].Size; got != "large" {
		t.Fatalf("service size = %q, want large", got)
	}
	out, _ := MarshalFile(f)
	if !strings.Contains(string(out), "size: large") {
		t.Fatalf("rendered kuso.yaml lacks size:\n%s", out)
	}
}

// A size declared on the service in the template wins over the manifest's.
func TestRenderTemplate_TemplateSizeWins(t *testing.T) {
	m := mustManifest(t)
	m.Size = "large"
	tmpl := "apiVersion: kuso/v1\nproject: x\nservices:\n  - name: app\n    runtime: image\n    size: small\n    image: { repository: ghcr.io/x/app, tag: \"1\" }\n"
	f, _, err := RenderTemplate(m, []byte(tmpl), "shop", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Services[0].Size; got != "small" {
		t.Fatalf("service size = %q, want small", got)
	}
}

// An unanswered prompt renders its manifest default: the CLI only sends
// --set keys and the web dialog only sends edited fields.
func TestRenderTemplate_UnansweredPromptUsesDefault(t *testing.T) {
	m := mustManifest(t,
		Prompt{Key: "host", Title: "Host", Kind: "domain", Required: true},
		Prompt{Key: "admin_email", Title: "Email", Kind: "string", Required: true, Default: "ops@example.com"},
	)
	for name, answers := range map[string]map[string]string{
		"absent": {"host": "app.example.com"},
		"empty":  {"host": "app.example.com", "admin_email": ""},
	} {
		f, _, err := RenderTemplate(m, []byte(tmplWithPrompt), "shop", answers)
		if err != nil {
			t.Fatalf("%s: unexpected err: %v", name, err)
		}
		if got := f.Services[0].Env["ADMIN_EMAIL"].Value; got != "ops@example.com" {
			t.Fatalf("%s: ADMIN_EMAIL = %q, want the default", name, got)
		}
	}
}
