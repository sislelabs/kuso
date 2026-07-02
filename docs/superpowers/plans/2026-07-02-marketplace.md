# One-click App Marketplace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a curated catalog of self-hostable apps that deploy in one click — pick an app, answer a few prompts, get a running service with its addons, a domain, a cert, and generated secrets — without hand-writing a kuso.yaml.

**Architecture:** A marketplace template is the existing `spec.File` (kuso.yaml) with `${{ prompt.<key> }}` placeholders, plus a `manifest.yaml` (metadata + prompt definitions). Both are `go:embed`ded into the server binary. A pure `marketplace` package (embed + parse + substitute, no kube calls) turns an app slug + answers into a rendered `*spec.File`. A read-only render endpoint returns the kuso.yaml; the actual create reuses the existing `POST /api/projects/{p}/apply` — exactly the convert-preview / apply split the compose importer uses, so diff/plan/prune and `DiffConfirmDialog` come for free.

**Tech Stack:** Go (server-go, `go:embed`, `gopkg.in/yaml.v3`, chi router), cobra CLI (`cli/cmd/kusoCli`), resty API client (`cli/pkg/kusoApi`), Next.js 16 App Router + React Query (web).

## Global Constraints

- **The `marketplace` package lives INSIDE `server-go`** (`server-go/internal/marketplace/`) and imports `server-go/internal/spec` directly. It does NOT carry a local kuso.yaml mirror — that is the compose module's pattern because `internal/` is not importable across Go modules; marketplace is in the same module, so it reuses `spec.File`/`spec.Parse` as the source of truth.
- **Render is pure and read-only.** The `marketplace` package makes no kube calls and no network calls. The server never writes during render; creation goes through the existing apply endpoint only.
- **Prompt substitution happens on the parsed `*spec.File` struct, not on YAML text.** Parse the template first, then replace `${{ prompt.<key> }}` inside already-parsed string fields. Answers can never change YAML structure (no injection).
- **Templates are CI-validated.** Every embedded template must `spec.Parse` cleanly, pin image tags (reject `:latest`), reference only declared prompts, declare every prompt it references, and use only supported addon kinds.
- **Handler mounted unconditionally** on the bearer-protected `/api/*` router (matching `ImportComposeHandler`), request body capped and rate-limited by the existing `/api/*` middleware. Render body cap: 1 MiB (matches compose). Render timeout: 15s.
- **Copy rule:** prompt `key` matches `^[a-z0-9_]+$`; app slug (= template dir name) matches `^[a-z0-9-]+$`.
- **v1 catalog is 8 apps**, all single-service `runtime: image` with pinned tags: uptime-kuma, umami, n8n, vaultwarden, gitea, metabase, plausible, listmonk.
- **Client type is `KusoClient`** (methods hang off `k *KusoClient`, HTTP via `k.client.Get(...)`, path escaping via `esc(...)`).

---

## File Structure

**Created:**
- `server-go/internal/marketplace/manifest.go` — `Manifest`, `Prompt` types + validation
- `server-go/internal/marketplace/catalog.go` — `go:embed` templates, parse-once, `List()`/`Get()`
- `server-go/internal/marketplace/render.go` — `Render(app, project, answers) (*spec.File, []Note, error)`
- `server-go/internal/marketplace/manifest_test.go`
- `server-go/internal/marketplace/catalog_test.go` — validates EVERY embedded template
- `server-go/internal/marketplace/render_test.go`
- `server-go/internal/marketplace/templates/<app>/manifest.yaml` ×8
- `server-go/internal/marketplace/templates/<app>/kuso.yaml` ×8
- `server-go/internal/marketplace/templates/<app>/icon.svg` ×8
- `server-go/internal/http/handlers/marketplace.go` — 4 routes
- `server-go/internal/http/handlers/marketplace_test.go`
- `cli/pkg/kusoApi/marketplace.go` — resty methods
- `cli/cmd/kusoCli/marketplace.go` — `list` / `info` / `deploy`
- `web/src/features/marketplace/api.ts`
- `web/src/features/marketplace/hooks.ts`
- `web/src/features/marketplace/index.ts`
- `web/src/app/(app)/marketplace/page.tsx`
- `web/src/components/marketplace/DeployDialog.tsx`

**Modified:**
- `server-go/internal/http/router.go` — mount the handler (near line 600, next to `ImportComposeHandler`)
- `web/src/components/layout/TopNav.tsx` — add a Marketplace link
- `docs/AGENT_SMOKE_TEST.md` — add the marketplace deploy smoke step

---

## Task 1: Manifest types + validation

**Files:**
- Create: `server-go/internal/marketplace/manifest.go`
- Test: `server-go/internal/marketplace/manifest_test.go`

**Interfaces:**
- Produces:
  - `type Prompt struct { Key, Title, Kind, Help, Default, Placeholder string; Required bool }`
  - `type Manifest struct { Name, Title, Description, Category, Website, AppVersion string; Prompts []Prompt }`
  - `func ParseManifest(raw []byte) (*Manifest, error)` — strict decode + validation
  - `var ErrInvalidManifest = errors.New("marketplace: invalid manifest")`
  - Valid prompt kinds: `string`, `password`, `domain`. Valid categories: `analytics`, `automation`, `monitoring`, `dev-tools`, `data`, `comms`.

- [ ] **Step 1: Write the failing test**

```go
package marketplace

import (
	"errors"
	"testing"
)

func TestParseManifest_Valid(t *testing.T) {
	raw := []byte(`name: umami
title: Umami
description: Privacy-friendly analytics.
category: analytics
website: https://umami.is
appVersion: "2.13"
prompts:
  - key: admin_email
    title: Admin email
    kind: string
    required: true
`)
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if m.Name != "umami" || m.Title != "Umami" || m.Category != "analytics" {
		t.Fatalf("bad manifest: %+v", m)
	}
	if len(m.Prompts) != 1 || m.Prompts[0].Key != "admin_email" || !m.Prompts[0].Required {
		t.Fatalf("bad prompts: %+v", m.Prompts)
	}
}

func TestParseManifest_Errors(t *testing.T) {
	cases := map[string]string{
		"missing name":      "title: X\ndescription: d\ncategory: data\n",
		"bad slug":          "name: Umami!\ntitle: X\ndescription: d\ncategory: data\n",
		"bad category":      "name: umami\ntitle: X\ndescription: d\ncategory: nope\n",
		"missing title":     "name: umami\ndescription: d\ncategory: data\n",
		"bad prompt key":    "name: umami\ntitle: X\ndescription: d\ncategory: data\nprompts:\n  - key: Bad-Key\n    title: T\n    kind: string\n",
		"bad prompt kind":   "name: umami\ntitle: X\ndescription: d\ncategory: data\nprompts:\n  - key: k\n    title: T\n    kind: secret\n",
		"dup prompt key":    "name: umami\ntitle: X\ndescription: d\ncategory: data\nprompts:\n  - key: k\n    title: A\n    kind: string\n  - key: k\n    title: B\n    kind: string\n",
		"unknown field":     "name: umami\ntitle: X\ndescription: d\ncategory: data\nbogus: 1\n",
	}
	for label, raw := range cases {
		if _, err := ParseManifest([]byte(raw)); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("%s: want ErrInvalidManifest, got %v", label, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/marketplace/ -run TestParseManifest -v`
Expected: FAIL — build error, `ParseManifest` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// Package marketplace turns a curated catalog of app templates into
// deployable kuso.yaml. A template is the existing spec.File shape with
// ${{ prompt.<key> }} placeholders plus a manifest (metadata + prompt
// definitions). The package is pure: embed + parse + substitute, no
// kube or network calls. Creation happens elsewhere, by feeding the
// rendered kuso.yaml through POST /api/projects/{p}/apply.
package marketplace

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// ErrInvalidManifest is returned for a malformed or invalid manifest.
var ErrInvalidManifest = errors.New("marketplace: invalid manifest")

// Prompt is one value the deployer must supply. Kind drives the UI
// widget: string (plain), password (masked), domain (hostname, the UI
// pre-fills <app>.<baseDomain>). Substituted where ${{ prompt.<Key> }}
// appears in the template.
type Prompt struct {
	Key         string `yaml:"key"`
	Title       string `yaml:"title"`
	Kind        string `yaml:"kind"`
	Help        string `yaml:"help,omitempty"`
	Default     string `yaml:"default,omitempty"`
	Placeholder string `yaml:"placeholder,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
}

// Manifest is the metadata + prompt schema for one catalog app.
type Manifest struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Category    string   `yaml:"category"`
	Website     string   `yaml:"website,omitempty"`
	AppVersion  string   `yaml:"appVersion,omitempty"`
	Prompts     []Prompt `yaml:"prompts,omitempty"`
}

var (
	slugRe      = regexp.MustCompile(`^[a-z0-9-]+$`)
	promptKeyRe = regexp.MustCompile(`^[a-z0-9_]+$`)
)

var validKinds = map[string]bool{"string": true, "password": true, "domain": true}
var validCategories = map[string]bool{
	"analytics": true, "automation": true, "monitoring": true,
	"dev-tools": true, "data": true, "comms": true,
}

// ParseManifest strictly decodes and validates a manifest.yaml.
func ParseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidManifest, err.Error())
	}
	if !slugRe.MatchString(m.Name) {
		return nil, fmt.Errorf("%w: name %q must match %s", ErrInvalidManifest, m.Name, slugRe)
	}
	if m.Title == "" {
		return nil, fmt.Errorf("%w: title is required", ErrInvalidManifest)
	}
	if m.Description == "" {
		return nil, fmt.Errorf("%w: description is required", ErrInvalidManifest)
	}
	if !validCategories[m.Category] {
		return nil, fmt.Errorf("%w: category %q is not a known category", ErrInvalidManifest, m.Category)
	}
	seen := map[string]bool{}
	for _, p := range m.Prompts {
		if !promptKeyRe.MatchString(p.Key) {
			return nil, fmt.Errorf("%w: prompt key %q must match %s", ErrInvalidManifest, p.Key, promptKeyRe)
		}
		if seen[p.Key] {
			return nil, fmt.Errorf("%w: duplicate prompt key %q", ErrInvalidManifest, p.Key)
		}
		seen[p.Key] = true
		if p.Title == "" {
			return nil, fmt.Errorf("%w: prompt %q needs a title", ErrInvalidManifest, p.Key)
		}
		if !validKinds[p.Kind] {
			return nil, fmt.Errorf("%w: prompt %q has invalid kind %q", ErrInvalidManifest, p.Key, p.Kind)
		}
	}
	return &m, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd server-go && go test ./internal/marketplace/ -run TestParseManifest -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/marketplace/manifest.go server-go/internal/marketplace/manifest_test.go
git commit -m "feat(marketplace): manifest types + strict validation"
```

---

## Task 2: Render engine (prompt substitution on the parsed spec.File)

**Files:**
- Create: `server-go/internal/marketplace/render.go`
- Test: `server-go/internal/marketplace/render_test.go`

**Interfaces:**
- Consumes: `Manifest`, `Prompt` (Task 1); `spec.File`, `spec.Parse` from `github.com/sislelabs/kuso/server-go/internal/spec`.
- Produces:
  - `type Note struct { Kind, Detail string }` (`Kind` ∈ `"service"`, `"addon"`, `"secret"`, `"domain"`, `"info"`)
  - `func RenderTemplate(m *Manifest, templateYAML []byte, project string, answers map[string]string) (*spec.File, []Note, error)`
  - `var ErrRender = errors.New("marketplace: render")`
  - Substitutes `${{ prompt.<key> }}` in every string field of the parsed `*spec.File` (service env values, domains, image tags, addon fields, cron fields, command args). Unknown token → `ErrRender`. Missing required answer → `ErrRender`. Sets `f.Project = project`.

Notes on substitution scope: walk `File.Services` (Name, Repo, Branch, Path, Command[], Domains[].Host, Env values, Image.Repository/Tag, Release.Command[]), `File.Addons` (all string fields), `File.Crons` (URL, Image, Command[], Service). Only `EnvValue.Value` is substituted (never `.Generate`).

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/marketplace/ -run TestRenderTemplate -v`
Expected: FAIL — `RenderTemplate` / `Note` / `ErrRender` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package marketplace

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/sislelabs/kuso/server-go/internal/spec"
)

// ErrRender is returned when a template can't be rendered (bad answers,
// unknown token, invalid resulting spec).
var ErrRender = errors.New("marketplace: render")

// Note is one human-readable line describing what a render produced,
// shown in the UI/CLI before apply.
type Note struct {
	Kind   string `json:"kind"`   // service | addon | secret | domain | info
	Detail string `json:"detail"`
}

// tokenRe matches ${{ prompt.<key> }} with flexible inner whitespace.
var tokenRe = regexp.MustCompile(`\$\{\{\s*prompt\.([a-z0-9_]+)\s*\}\}`)

// RenderTemplate parses the template, substitutes ${{ prompt.<key> }}
// tokens with answers, sets the project, re-validates, and returns the
// spec.File plus notes. Substitution runs on the PARSED struct so an
// answer can never alter YAML structure.
func RenderTemplate(m *Manifest, templateYAML []byte, project string, answers map[string]string) (*spec.File, []Note, error) {
	if project == "" {
		return nil, nil, fmt.Errorf("%w: project is required", ErrRender)
	}
	// Validate answers against the manifest: required present, and only
	// declared keys accepted.
	declared := map[string]Prompt{}
	for _, p := range m.Prompts {
		declared[p.Key] = p
	}
	for k := range answers {
		if _, ok := declared[k]; !ok {
			return nil, nil, fmt.Errorf("%w: unknown answer %q", ErrRender, k)
		}
	}
	for _, p := range m.Prompts {
		v, ok := answers[p.Key]
		if (!ok || v == "") && p.Required {
			return nil, nil, fmt.Errorf("%w: missing required answer %q", ErrRender, p.Key)
		}
	}

	f, err := spec.Parse(templateYAML)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: template does not parse: %s", ErrRender, err.Error())
	}

	// substitute replaces every token in s; an unknown token (no matching
	// declared prompt) is a hard error to keep template typos loud.
	var subErr error
	substitute := func(s string) string {
		return tokenRe.ReplaceAllStringFunc(s, func(match string) string {
			key := tokenRe.FindStringSubmatch(match)[1]
			if _, ok := declared[key]; !ok {
				subErr = fmt.Errorf("%w: template references undeclared prompt %q", ErrRender, key)
				return match
			}
			return answers[key] // "" for optional-unanswered, which is fine
		})
	}

	f.Project = project
	for si := range f.Services {
		s := &f.Services[si]
		s.Name = substitute(s.Name)
		s.Repo = substitute(s.Repo)
		s.Branch = substitute(s.Branch)
		s.Path = substitute(s.Path)
		for i := range s.Command {
			s.Command[i] = substitute(s.Command[i])
		}
		for di := range s.Domains {
			s.Domains[di].Host = substitute(s.Domains[di].Host)
		}
		for k, ev := range s.Env {
			if ev.Generate == "" {
				ev.Value = substitute(ev.Value)
				s.Env[k] = ev
			}
		}
		if s.Image != nil {
			s.Image.Repository = substitute(s.Image.Repository)
			s.Image.Tag = substitute(s.Image.Tag)
		}
		if s.Release != nil {
			for i := range s.Release.Command {
				s.Release.Command[i] = substitute(s.Release.Command[i])
			}
		}
	}
	for ai := range f.Addons {
		a := &f.Addons[ai]
		a.Name = substitute(a.Name)
		a.Version = substitute(a.Version)
		a.Database = substitute(a.Database)
	}
	for ci := range f.Crons {
		c := &f.Crons[ci]
		c.Name = substitute(c.Name)
		c.URL = substitute(c.URL)
		c.Image = substitute(c.Image)
		c.Service = substitute(c.Service)
		for i := range c.Command {
			c.Command[i] = substitute(c.Command[i])
		}
	}
	if subErr != nil {
		return nil, nil, subErr
	}

	// Re-validate the substituted document — round-trip through Parse so
	// the returned File is guaranteed apply-able.
	out, err := MarshalFile(f)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrRender, err.Error())
	}
	if _, err := spec.Parse(out); err != nil {
		return nil, nil, fmt.Errorf("%w: rendered spec invalid: %s", ErrRender, err.Error())
	}

	return f, buildNotes(f), nil
}

func buildNotes(f *spec.File) []Note {
	var notes []Note
	for _, s := range f.Services {
		notes = append(notes, Note{Kind: "service", Detail: fmt.Sprintf("service %s will be created", s.Name)})
		var gen int
		var hosts []string
		for _, ev := range s.Env {
			if ev.IsGenerated() {
				gen++
			}
		}
		for _, d := range s.Domains {
			hosts = append(hosts, d.Host)
		}
		if gen > 0 {
			notes = append(notes, Note{Kind: "secret", Detail: fmt.Sprintf("%d secret(s) generated for %s", gen, s.Name)})
		}
		sort.Strings(hosts)
		for _, h := range hosts {
			notes = append(notes, Note{Kind: "domain", Detail: fmt.Sprintf("domain %s", h)})
		}
	}
	for _, a := range f.Addons {
		v := a.Kind
		if a.Version != "" {
			v = a.Kind + " " + a.Version
		}
		notes = append(notes, Note{Kind: "addon", Detail: fmt.Sprintf("addon %s (%s) will be created", a.Name, v)})
	}
	return notes
}

// MarshalFile renders a spec.File back to kuso.yaml bytes. The spec
// package has no exported Marshal, but its types carry yaml tags and
// EnvValue.MarshalYAML, so yaml.Marshal round-trips faithfully.
func MarshalFile(f *spec.File) ([]byte, error) { return yaml.Marshal(f) }
```

Add `"gopkg.in/yaml.v3"` to this file's imports (used by both `MarshalFile` and the re-validation round-trip). Replace the earlier `yamlMarshal(f)` call inside `RenderTemplate` with `MarshalFile(f)` so there is exactly one marshal helper.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd server-go && go test ./internal/marketplace/ -run TestRenderTemplate -v && go vet ./internal/marketplace/`
Expected: PASS (all four subtests), vet clean.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/marketplace/render.go server-go/internal/marketplace/render_test.go
git commit -m "feat(marketplace): render engine — post-parse prompt substitution"
```

---

## Task 3: Catalog (embed + parse-once + list/get)

**Files:**
- Create: `server-go/internal/marketplace/catalog.go`
- Create: `server-go/internal/marketplace/templates/uptime-kuma/{manifest.yaml,kuso.yaml,icon.svg}` (one real template so embed compiles + tests have data)
- Test: `server-go/internal/marketplace/catalog_test.go`

**Interfaces:**
- Consumes: `Manifest`, `ParseManifest` (Task 1); `RenderTemplate` (Task 2); `spec.Parse`.
- Produces:
  - `type Entry struct { Manifest *Manifest; TemplateYAML []byte; Icon []byte }`
  - `func Catalog() ([]*Manifest, error)` — sorted by Title
  - `func GetEntry(slug string) (*Entry, error)` — `ErrNotFound` for unknown slug
  - `var ErrNotFound = errors.New("marketplace: app not found")`
  - `func Validate() error` — validates ALL embedded templates (used by the CI test)

- [ ] **Step 1: Write the failing test**

```go
package marketplace

import (
	"errors"
	"strings"
	"testing"

	"github.com/sislelabs/kuso/server-go/internal/spec"
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
```

- [ ] **Step 2: Create the first real template so embed has data**

`server-go/internal/marketplace/templates/uptime-kuma/manifest.yaml`:

```yaml
name: uptime-kuma
title: Uptime Kuma
description: Self-hosted uptime monitoring with a beautiful dashboard.
category: monitoring
website: https://uptime.kuma.pet
appVersion: "1.23.16"
prompts:
  - key: host
    title: Domain
    kind: domain
    required: true
    help: The hostname Uptime Kuma will be served on.
```

`server-go/internal/marketplace/templates/uptime-kuma/kuso.yaml`:

```yaml
apiVersion: kuso/v1
project: uptime-kuma
services:
  - name: uptime-kuma
    runtime: image
    port: 3001
    image:
      repository: louislam/uptime-kuma
      tag: "1.23.16"
    domains:
      - host: "${{ prompt.host }}"
        tls: true
    volumes:
      - name: data
        mountPath: /app/data
        sizeGi: 2
```

`server-go/internal/marketplace/templates/uptime-kuma/icon.svg`: a small valid SVG (e.g. a 24×24 `<svg>…</svg>` placeholder logo).

- [ ] **Step 3: Run test to verify it fails**

Run: `cd server-go && go test ./internal/marketplace/ -run 'TestCatalog|TestGetEntry' -v`
Expected: FAIL — `Catalog`/`GetEntry`/`Entry`/`ErrNotFound` undefined.

- [ ] **Step 4: Write minimal implementation**

```go
package marketplace

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"sync"
)

//go:embed templates
var templatesFS embed.FS

// ErrNotFound is returned when a slug has no template.
var ErrNotFound = errors.New("marketplace: app not found")

// Entry is one fully-loaded catalog app.
type Entry struct {
	Manifest     *Manifest
	TemplateYAML []byte
	Icon         []byte
}

var (
	loadOnce sync.Once
	loaded   map[string]*Entry
	loadErr  error
)

func load() {
	loaded = map[string]*Entry{}
	dirs, err := templatesFS.ReadDir("templates")
	if err != nil {
		loadErr = err
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		slug := d.Name()
		base := path.Join("templates", slug)
		mraw, err := fs.ReadFile(templatesFS, path.Join(base, "manifest.yaml"))
		if err != nil {
			loadErr = fmt.Errorf("%s: read manifest: %w", slug, err)
			return
		}
		m, err := ParseManifest(mraw)
		if err != nil {
			loadErr = fmt.Errorf("%s: %w", slug, err)
			return
		}
		if m.Name != slug {
			loadErr = fmt.Errorf("%s: manifest name %q must equal directory name", slug, m.Name)
			return
		}
		tmpl, err := fs.ReadFile(templatesFS, path.Join(base, "kuso.yaml"))
		if err != nil {
			loadErr = fmt.Errorf("%s: read kuso.yaml: %w", slug, err)
			return
		}
		icon, _ := fs.ReadFile(templatesFS, path.Join(base, "icon.svg")) // optional
		loaded[slug] = &Entry{Manifest: m, TemplateYAML: tmpl, Icon: icon}
	}
}

// Catalog returns all app manifests, sorted by title.
func Catalog() ([]*Manifest, error) {
	loadOnce.Do(load)
	if loadErr != nil {
		return nil, loadErr
	}
	out := make([]*Manifest, 0, len(loaded))
	for _, e := range loaded {
		out = append(out, e.Manifest)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}

// GetEntry returns one loaded template by slug.
func GetEntry(slug string) (*Entry, error) {
	loadOnce.Do(load)
	if loadErr != nil {
		return nil, loadErr
	}
	e, ok := loaded[slug]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	return e, nil
}

// Validate loads every template and confirms it parses + renders with
// stub required answers. Used by the CI test and can be called at boot
// as a fail-fast.
func Validate() error {
	_, err := Catalog()
	return err
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd server-go && go test ./internal/marketplace/ -v`
Expected: PASS (all tasks 1–3 tests).

- [ ] **Step 6: Commit**

```bash
git add server-go/internal/marketplace/
git commit -m "feat(marketplace): embedded catalog + first template (uptime-kuma)"
```

---

## Task 4: HTTP handler (list / get / icon / render)

**Files:**
- Create: `server-go/internal/http/handlers/marketplace.go`
- Test: `server-go/internal/http/handlers/marketplace_test.go`
- Modify: `server-go/internal/http/router.go` (mount, near line 600)

**Interfaces:**
- Consumes: `marketplace.Catalog`, `marketplace.GetEntry`, `marketplace.RenderTemplate`, `marketplace.MarshalFile`, `marketplace.ErrNotFound`, `marketplace.ErrRender`.
- Produces (wire shapes):
  - `GET /api/marketplace` → `{ apps: [{name,title,description,category,website,appVersion,prompts:[...]}] }`
  - `GET /api/marketplace/{app}` → single manifest (same shape as one `apps` element)
  - `GET /api/marketplace/{app}/icon` → `image/svg+xml` bytes
  - `POST /api/marketplace/{app}/render` body `{project, answers:{k:v}}` → `{project, yaml, notes:[{kind,detail}]}`
- Uses existing helpers in the `handlers` package: `writeJSON(w, status, v)` (seen in `import_compose.go`), `chi.URLParam(r, "app")`.

- [ ] **Step 1: Write the failing test**

```go
package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func mktRouter() *chi.Mux {
	r := chi.NewRouter()
	(&MarketplaceHandler{}).Mount(r)
	return r
}

func TestMarketplace_List(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/marketplace", nil)
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var body struct {
		Apps []struct {
			Name  string `json:"name"`
			Title string `json:"title"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Apps) == 0 {
		t.Fatal("empty apps")
	}
}

func TestMarketplace_Render_OK(t *testing.T) {
	payload := `{"project":"mysite","answers":{"host":"mysite.example.com"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/marketplace/uptime-kuma/render", strings.NewReader(payload))
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		YAML  string `json:"yaml"`
		Notes []struct{ Kind, Detail string } `json:"notes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body.YAML, "mysite.example.com") {
		t.Fatalf("host not substituted in yaml: %s", body.YAML)
	}
	if !strings.Contains(body.YAML, "project: mysite") {
		t.Fatalf("project not set in yaml: %s", body.YAML)
	}
}

func TestMarketplace_Render_MissingRequired(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/marketplace/uptime-kuma/render",
		strings.NewReader(`{"project":"mysite","answers":{}}`))
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestMarketplace_Render_UnknownApp(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/marketplace/nope/render",
		strings.NewReader(`{"project":"x","answers":{}}`))
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/http/handlers/ -run TestMarketplace -v`
Expected: FAIL — `MarketplaceHandler` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sislelabs/kuso/server-go/internal/marketplace"
)

// MarketplaceHandler serves the embedded app catalog and a read-only
// render endpoint. It writes nothing: the UI/CLI feed the rendered
// kuso.yaml back through POST /api/projects/{p}/apply, mirroring the
// compose importer.
type MarketplaceHandler struct {
	Logger *slog.Logger
}

func (h *MarketplaceHandler) Mount(r chi.Router) {
	r.Get("/api/marketplace", h.List)
	r.Get("/api/marketplace/{app}", h.Get)
	r.Get("/api/marketplace/{app}/icon", h.Icon)
	r.Post("/api/marketplace/{app}/render", h.Render)
}

func (h *MarketplaceHandler) List(w http.ResponseWriter, r *http.Request) {
	apps, err := marketplace.Catalog()
	if err != nil {
		h.log().Error("marketplace catalog", "err", err)
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

func (h *MarketplaceHandler) Get(w http.ResponseWriter, r *http.Request) {
	e, err := marketplace.GetEntry(chi.URLParam(r, "app"))
	if errors.Is(err, marketplace.ErrNotFound) {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, e.Manifest)
}

func (h *MarketplaceHandler) Icon(w http.ResponseWriter, r *http.Request) {
	e, err := marketplace.GetEntry(chi.URLParam(r, "app"))
	if errors.Is(err, marketplace.ErrNotFound) || len(e.Icon) == 0 {
		http.Error(w, "no icon", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(e.Icon)
}

// MarketplaceRenderRequest is the render wire shape.
type MarketplaceRenderRequest struct {
	Project string            `json:"project"`
	Answers map[string]string `json:"answers"`
}

// MarketplaceRenderResponse carries the rendered kuso.yaml + notes.
type MarketplaceRenderResponse struct {
	Project string             `json:"project"`
	YAML    string             `json:"yaml"`
	Notes   []marketplace.Note `json:"notes"`
}

func (h *MarketplaceHandler) Render(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req MarketplaceRenderRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	e, err := marketplace.GetEntry(chi.URLParam(r, "app"))
	if errors.Is(err, marketplace.ErrNotFound) {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	f, notes, err := marketplace.RenderTemplate(e.Manifest, e.TemplateYAML, req.Project, req.Answers)
	if errors.Is(err, marketplace.ErrRender) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		h.log().Error("marketplace render", "app", e.Manifest.Name, "err", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	out, err := marketplace.MarshalFile(f)
	if err != nil {
		http.Error(w, "render kuso.yaml failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, MarketplaceRenderResponse{
		Project: req.Project, YAML: string(out), Notes: notes,
	})
}

func (h *MarketplaceHandler) log() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}
```

- [ ] **Step 4: Run handler test to verify it passes**

Run: `cd server-go && go test ./internal/http/handlers/ -run TestMarketplace -v`
Expected: PASS (all four subtests).

- [ ] **Step 5: Mount in router**

In `server-go/internal/http/router.go`, immediately after the `ImportComposeHandler` mount (~line 600):

```go
		// App marketplace — read-only catalog + render. The UI/CLI feed
		// the rendered kuso.yaml back through POST /api/projects/{p}/apply.
		(&httphandlers.MarketplaceHandler{Logger: d.Logger}).Mount(r)
```

- [ ] **Step 6: Build + full handler test**

Run: `cd server-go && go build ./... && go test ./internal/http/handlers/ -run TestMarketplace`
Expected: build OK, PASS.

- [ ] **Step 7: Commit**

```bash
git add server-go/internal/http/handlers/marketplace.go server-go/internal/http/handlers/marketplace_test.go server-go/internal/http/router.go
git commit -m "feat(marketplace): HTTP handler (list/get/icon/render) + mount"
```

---

## Task 5: Remaining 7 templates

**Files:**
- Create: `server-go/internal/marketplace/templates/{umami,n8n,vaultwarden,gitea,metabase,plausible,listmonk}/{manifest.yaml,kuso.yaml,icon.svg}`

**Interfaces:**
- Consumes: the catalog CI test from Task 3 (`TestCatalog_AllTemplatesValid`) validates each new template automatically. No new Go code.

Authoring rules (enforced by the CI test): pin image tags (no `:latest`), every `${{ prompt.* }}` token has a matching declared prompt, every declared prompt is referenced, addon `kind` ∈ supported set (`postgres`, `redis`, `valkey`, `clickhouse`, `mongodb`, `nats`, `rabbitmq`, `redpanda`, `meilisearch`, `s3`, `mailpit`, `public-tcp`), secrets via `{generate: hex32}`, DB wiring via `${{ <addon>.DATABASE_URL }}` refs.

- [ ] **Step 1: umami** — `manifest.yaml` (category `analytics`, one `domain` prompt `host`), `kuso.yaml`: one `runtime: image` service `ghcr.io/umami-software/umami:postgresql-v2.13.3`, port 3000, `env: DATABASE_URL: "${{ umami-db.DATABASE_URL }}"`, `APP_SECRET: { generate: hex32 }`, domain `${{ prompt.host }}`; addon `umami-db` kind `postgres` version `16`. icon.svg.

- [ ] **Step 2: n8n** — category `automation`, `domain` prompt `host`. Service `docker.n8n.io/n8nio/n8n:1.62.1`, port 5678, env `DB_TYPE: postgresdb`, `DB_POSTGRESDB_*` from `${{ n8n-db.HOST/PORT/USER/PASSWORD/DATABASE }}` refs (confirm the conn-secret keys via `kuso get addons` on a live postgres; adjust ref names to the actual keys), `N8N_ENCRYPTION_KEY: { generate: hex32 }`, `N8N_HOST: "${{ prompt.host }}"`, `WEBHOOK_URL: "https://${{ prompt.host }}/"`, domain `${{ prompt.host }}`, volume `/home/node/.n8n` 1Gi; addon `n8n-db` postgres 16.

- [ ] **Step 3: vaultwarden** — category `dev-tools`, `domain` prompt `host`. Service `vaultwarden/server:1.32.1`, port 80, `env: DOMAIN: "https://${{ prompt.host }}"`, `ADMIN_TOKEN: { generate: hex32 }`, domain `${{ prompt.host }}`, volume `/data` 2Gi. No addon.

- [ ] **Step 4: gitea** — category `dev-tools`, `domain` prompt `host`. Service `gitea/gitea:1.22.3`, port 3000, env `GITEA__database__DB_TYPE: postgres` + `GITEA__database__HOST/NAME/USER/PASSWD` from `gitea-db` refs, `GITEA__server__ROOT_URL: "https://${{ prompt.host }}/"`, domain `${{ prompt.host }}`, volume `/data` 5Gi; addon `gitea-db` postgres 16.

- [ ] **Step 5: metabase** — category `data`, `domain` prompt `host`. Service `metabase/metabase:v0.51.1.4`, port 3000, env `MB_DB_TYPE: postgres` + `MB_DB_CONNECTION_URI: "${{ metabase-db.DATABASE_URL }}"`, domain `${{ prompt.host }}`; addon `metabase-db` postgres 16.

- [ ] **Step 6: plausible** — category `analytics`, `domain` prompt `host`. Service `ghcr.io/plausible/community-edition:v2.1.4`, port 8000, env `BASE_URL: "https://${{ prompt.host }}"`, `SECRET_KEY_BASE: { generate: hex32 }`, `DATABASE_URL: "${{ plausible-db.DATABASE_URL }}"`, `CLICKHOUSE_DATABASE_URL: "${{ plausible-ch.URL }}"` (confirm clickhouse conn-secret key name on a live addon), domain `${{ prompt.host }}`; addons `plausible-db` postgres 16 + `plausible-ch` clickhouse. **This is the multi-addon exerciser.**

- [ ] **Step 7: listmonk** — category `comms`, `domain` prompt `host`. Service `listmonk/listmonk:v3.0.0`, port 9000, env `LISTMONK_app__address: "0.0.0.0:9000"`, DB from `listmonk-db` refs, domain `${{ prompt.host }}`; addon `listmonk-db` postgres 16.

- [ ] **Step 8: Validate all templates**

Run: `cd server-go && go test ./internal/marketplace/ -run TestCatalog -v`
Expected: PASS — 8 apps, all valid. If a template fails (undeclared token / unpinned tag / dead prompt / parse error), fix that template and re-run.

- [ ] **Step 9: Commit**

```bash
git add server-go/internal/marketplace/templates/
git commit -m "feat(marketplace): v1 catalog — umami, n8n, vaultwarden, gitea, metabase, plausible, listmonk"
```

---

## Task 6: CLI (`kuso marketplace list|info|deploy`)

**Files:**
- Create: `cli/pkg/kusoApi/marketplace.go`
- Create: `cli/cmd/kusoCli/marketplace.go`

**Interfaces:**
- Consumes: `KusoClient` (`k.client.Get`, `esc`), `k.CreateProject`, `k.ApplyConfig` (existing in `projects.go`).
- Produces (kusoApi):
  - `func (k *KusoClient) MarketplaceList() (*resty.Response, error)` → GET `/api/marketplace`
  - `func (k *KusoClient) MarketplaceGet(app string) (*resty.Response, error)` → GET `/api/marketplace/{app}`
  - `func (k *KusoClient) MarketplaceRender(app, project string, answers map[string]string) (*resty.Response, error)` → POST `/api/marketplace/{app}/render`
- Produces (cobra): `marketplace` command with `list`, `info <app>`, `deploy <app>` subcommands; `deploy` flags `--project`, `--set key=val` (repeatable), `--dry-run`. Mirrors `import.go`'s apply flow (CreateProject 409-tolerant, then ApplyConfig).

- [ ] **Step 1: kusoApi methods**

`cli/pkg/kusoApi/marketplace.go`:

```go
package kusoApi

import (
	"encoding/json"

	"github.com/go-resty/resty/v2"
)

// MarketplaceList GETs the app catalog.
func (k *KusoClient) MarketplaceList() (*resty.Response, error) {
	return k.client.Get("/api/marketplace")
}

// MarketplaceGet GETs one app's manifest (metadata + prompts).
func (k *KusoClient) MarketplaceGet(app string) (*resty.Response, error) {
	return k.client.Get("/api/marketplace/" + esc(app))
}

// MarketplaceRender POSTs answers and returns the rendered kuso.yaml.
// Matches the AddProjectCron idiom: SetBody on the shared client, then
// Post. resty marshals the struct to JSON.
func (k *KusoClient) MarketplaceRender(app, project string, answers map[string]string) (*resty.Response, error) {
	k.client.SetBody(map[string]any{"project": project, "answers": answers})
	return k.client.Post("/api/marketplace/" + esc(app) + "/render")
}
```

Drop the `encoding/json` import — `SetBody` handles marshaling. Keep only `github.com/go-resty/resty/v2`.

- [ ] **Step 2: cobra command**

`cli/cmd/kusoCli/marketplace.go`:

```go
package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var (
	mktProject string
	mktSets    []string
	mktDryRun  bool
)

var marketplaceCmd = &cobra.Command{
	Use:   "marketplace",
	Short: "Browse and deploy curated one-click apps",
}

var marketplaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available marketplace apps",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("run `kuso login` first")
		}
		resp, err := api.MarketplaceList()
		if err != nil {
			return err
		}
		var body struct {
			Apps []struct {
				Name, Title, Category, Description string
			} `json:"apps"`
		}
		if err := json.Unmarshal(resp.Body(), &body); err != nil {
			return err
		}
		for _, a := range body.Apps {
			fmt.Printf("%-16s %-12s %s\n", a.Name, a.Category, a.Title)
		}
		return nil
	},
}

var marketplaceInfoCmd = &cobra.Command{
	Use:   "info <app>",
	Short: "Show an app's details and required prompts",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("run `kuso login` first")
		}
		resp, err := api.MarketplaceGet(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("app %q not found", args[0])
		}
		fmt.Println(string(resp.Body()))
		return nil
	},
}

var marketplaceDeployCmd = &cobra.Command{
	Use:   "deploy <app>",
	Short: "Deploy a marketplace app",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso marketplace deploy uptime-kuma --set host=status.example.com
  kuso marketplace deploy umami --project analytics --set host=stats.example.com --dry-run`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("run `kuso login` first")
		}
		app := args[0]
		answers := map[string]string{}
		for _, kv := range mktSets {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("--set expects key=value, got %q", kv)
			}
			answers[k] = v
		}
		project := mktProject
		if project == "" {
			project = app
		}
		resp, err := api.MarketplaceRender(app, project, answers)
		if err != nil {
			return err
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("app %q not found", app)
		}
		if resp.StatusCode() >= 400 {
			return fmt.Errorf("render failed (%d): %s", resp.StatusCode(), resp.String())
		}
		var rendered struct {
			YAML  string `json:"yaml"`
			Notes []struct{ Kind, Detail string } `json:"notes"`
		}
		if err := json.Unmarshal(resp.Body(), &rendered); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "## Plan")
		for _, n := range rendered.Notes {
			fmt.Fprintf(os.Stderr, "  - [%s] %s\n", n.Kind, n.Detail)
		}
		fmt.Fprintln(os.Stderr)

		if mktDryRun {
			fmt.Print(rendered.YAML)
			fmt.Fprintln(os.Stderr, "\n→ dry-run only — drop --dry-run to create resources")
			return nil
		}
		// Ensure the project exists (spec.Apply doesn't create it). 409 ok.
		pr, err := api.CreateProject(kusoApi.CreateProjectRequest{Name: project})
		if err != nil {
			return fmt.Errorf("create project: %w", err)
		}
		if pr.StatusCode() >= 300 && pr.StatusCode() != 409 {
			return fmt.Errorf("create project failed (%d): %s", pr.StatusCode(), pr.String())
		}
		ar, err := api.ApplyConfig(project, []byte(rendered.YAML), false, false)
		if err != nil {
			return fmt.Errorf("apply: %w", err)
		}
		if ar.StatusCode() >= 400 {
			return fmt.Errorf("apply failed (%d): %s", ar.StatusCode(), ar.String())
		}
		fmt.Printf("→ deployed %s into project %s\n", app, project)
		return nil
	},
}

func init() {
	marketplaceDeployCmd.Flags().StringVar(&mktProject, "project", "", "target project (default: app name)")
	marketplaceDeployCmd.Flags().StringArrayVar(&mktSets, "set", nil, "prompt answer key=value (repeatable)")
	marketplaceDeployCmd.Flags().BoolVar(&mktDryRun, "dry-run", false, "render + print the kuso.yaml without creating anything")
	marketplaceCmd.AddCommand(marketplaceListCmd, marketplaceInfoCmd, marketplaceDeployCmd)
	rootCmd.AddCommand(marketplaceCmd)
}
```

- [ ] **Step 3: Build the CLI**

Run: `cd cli && go build -o /tmp/kuso ./cmd && /tmp/kuso marketplace --help`
Expected: build OK; help lists `list`, `info`, `deploy`.

- [ ] **Step 4: Commit**

```bash
git add cli/pkg/kusoApi/marketplace.go cli/cmd/kusoCli/marketplace.go
git commit -m "feat(cli): kuso marketplace list/info/deploy"
```

---

## Task 7: Web — feature client + hooks

**Files:**
- Create: `web/src/features/marketplace/api.ts`
- Create: `web/src/features/marketplace/hooks.ts`
- Create: `web/src/features/marketplace/index.ts`

**Interfaces:**
- Consumes: `api<T>` from `@/lib/api-client`; `applyConfig` from `@/features/projects` (existing, exported, signature `applyConfig(project, body, dryRun)`). NOTE: `createProject` exists in `web/src/features/projects/mutations.ts` but is NOT exported from the feature index — do NOT import it. Create the project by calling `api("/api/projects", { method: "POST", body: { name: project } })` directly and treating a 409 as success (see DeployDialog below).
- Produces:
  - `interface MarketplacePrompt { key,title,kind,help?,default?,placeholder?,required? }`
  - `interface MarketplaceApp { name,title,description,category,website?,appVersion?,prompts:MarketplacePrompt[] }`
  - `interface RenderResult { project:string; yaml:string; notes:{kind:string;detail:string}[] }`
  - `listMarketplace(): Promise<MarketplaceApp[]>`
  - `renderApp(app, project, answers): Promise<RenderResult>`
  - hooks `useMarketplace()`, `useRenderApp()`

- [ ] **Step 1: api.ts**

```ts
import { api } from "@/lib/api-client";

export interface MarketplacePrompt {
  key: string;
  title: string;
  kind: "string" | "password" | "domain";
  help?: string;
  default?: string;
  placeholder?: string;
  required?: boolean;
}

export interface MarketplaceApp {
  name: string;
  title: string;
  description: string;
  category: string;
  website?: string;
  appVersion?: string;
  prompts: MarketplacePrompt[];
}

export interface RenderResult {
  project: string;
  yaml: string;
  notes: { kind: string; detail: string }[];
}

export async function listMarketplace(): Promise<MarketplaceApp[]> {
  const res = await api<{ apps: MarketplaceApp[] }>("/api/marketplace");
  return res.apps ?? [];
}

export async function renderApp(
  app: string,
  project: string,
  answers: Record<string, string>,
): Promise<RenderResult> {
  return api<RenderResult>(`/api/marketplace/${encodeURIComponent(app)}/render`, {
    method: "POST",
    body: { project, answers },
  });
}
```

- [ ] **Step 2: hooks.ts**

```ts
import { useMutation, useQuery } from "@tanstack/react-query";
import { listMarketplace, renderApp } from "./api";

export function useMarketplace() {
  return useQuery({ queryKey: ["marketplace"], queryFn: listMarketplace });
}

export function useRenderApp(app: string) {
  return useMutation({
    mutationFn: (vars: { project: string; answers: Record<string, string> }) =>
      renderApp(app, vars.project, vars.answers),
  });
}
```

- [ ] **Step 3: index.ts**

```ts
export * from "./api";
export * from "./hooks";
```

- [ ] **Step 4: Typecheck**

Run: `cd web && npx tsc --noEmit`
Expected: no new errors from these files.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/marketplace/
git commit -m "feat(web): marketplace feature client + hooks"
```

---

## Task 8: Web — catalog page + deploy dialog

**Files:**
- Create: `web/src/app/(app)/marketplace/page.tsx`
- Create: `web/src/components/marketplace/DeployDialog.tsx`
- Modify: `web/src/components/layout/TopNav.tsx` (add Marketplace link)

**Interfaces:**
- Consumes: `useMarketplace`, `useRenderApp`, `MarketplaceApp` from `@/features/marketplace`; `applyConfig` + `createProject` from `@/features/projects`; `DiffConfirmDialog` from `@/components/shared/DiffConfirmDialog` (optional — a simpler confirm is acceptable for v1); `Button`, `Input` from `@/components/ui/*`; `useRouter` from `next/navigation`; `toast` from `sonner`.
- Produces: a `/marketplace` route (card grid + category filter/search) and a `DeployDialog` that collects project + prompt answers, renders, previews notes, and on confirm creates the project (409-tolerant) then applies, then routes to `/projects/{project}`.

- [ ] **Step 1: DeployDialog.tsx**

A dialog taking `{ app: MarketplaceApp; onClose: () => void }`. State: `project` (default `app.name`), `answers` keyed by prompt key (domain-kind prompts pre-filled empty; leave baseDomain auto-fill to a follow-up). On "Preview": call `useRenderApp(app.name).mutateAsync({project, answers})`, show `result.notes` as a list. On "Deploy": `createProject({name: project})` swallowing 409, then `applyConfig(project, result.yaml, false)`, then `toast.success` + `router.push('/projects/' + encodeURIComponent(project))`. Password-kind prompts use `<input type="password">`; required prompts block Preview when empty.

```tsx
"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api-client";
import { useRenderApp, type MarketplaceApp, type RenderResult } from "@/features/marketplace";
import { applyConfig } from "@/features/projects";

export function DeployDialog({ app, onClose }: { app: MarketplaceApp; onClose: () => void }) {
  const router = useRouter();
  const [project, setProject] = useState(app.name);
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [preview, setPreview] = useState<RenderResult | null>(null);
  const [deploying, setDeploying] = useState(false);
  const render = useRenderApp(app.name);

  const missing = app.prompts.some((p) => p.required && !answers[p.key]);

  async function onPreview() {
    try {
      setPreview(await render.mutateAsync({ project, answers }));
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function onDeploy() {
    if (!preview) return;
    setDeploying(true);
    try {
      try {
        // spec.Apply doesn't create the project; create it first.
        // 409 already-exists is fine; anything else re-throws.
        await api("/api/projects", { method: "POST", body: { name: project } });
      } catch (e) {
        if (!/409|exists/i.test((e as Error).message)) throw e;
      }
      await applyConfig(project, preview.yaml, false);
      toast.success(`Deployed ${app.title}`);
      router.push(`/projects/${encodeURIComponent(project)}`);
    } catch (e) {
      toast.error((e as Error).message);
      setDeploying(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div className="w-full max-w-lg rounded-lg bg-[var(--surface)] p-5" onClick={(e) => e.stopPropagation()}>
        <h2 className="text-lg font-semibold">Deploy {app.title}</h2>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">{app.description}</p>

        <label className="mt-4 block text-sm">Project</label>
        <Input value={project} onChange={(e) => setProject(e.target.value)} />

        {app.prompts.map((p) => (
          <div key={p.key} className="mt-3">
            <label className="block text-sm">
              {p.title}
              {p.required && <span className="text-amber-400"> *</span>}
            </label>
            <Input
              type={p.kind === "password" ? "password" : "text"}
              placeholder={p.placeholder}
              value={answers[p.key] ?? p.default ?? ""}
              onChange={(e) => setAnswers({ ...answers, [p.key]: e.target.value })}
            />
            {p.help && <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">{p.help}</p>}
          </div>
        ))}

        {preview && (
          <div className="mt-4 rounded border border-[var(--border)] p-3 text-sm">
            <p className="mb-1 font-medium">This will create:</p>
            <ul className="space-y-0.5">
              {preview.notes.map((n, i) => (
                <li key={i} className="text-[var(--text-secondary)]">
                  <span className="text-[var(--text-tertiary)]">[{n.kind}]</span> {n.detail}
                </li>
              ))}
            </ul>
          </div>
        )}

        <div className="mt-5 flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          {!preview ? (
            <Button onClick={onPreview} disabled={missing || render.isPending}>
              {render.isPending ? "Rendering…" : "Preview"}
            </Button>
          ) : (
            <Button onClick={onDeploy} disabled={deploying}>
              {deploying ? "Deploying…" : "Deploy"}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: page.tsx**

```tsx
"use client";

import { useState } from "react";
import { useMarketplace, type MarketplaceApp } from "@/features/marketplace";
import { DeployDialog } from "@/components/marketplace/DeployDialog";
import { env } from "@/lib/env";
import { Input } from "@/components/ui/input";

export default function MarketplacePage() {
  const { data: apps = [], isLoading } = useMarketplace();
  const [q, setQ] = useState("");
  const [cat, setCat] = useState<string>("all");
  const [selected, setSelected] = useState<MarketplaceApp | null>(null);

  const categories = ["all", ...Array.from(new Set(apps.map((a) => a.category))).sort()];
  const filtered = apps.filter(
    (a) =>
      (cat === "all" || a.category === cat) &&
      (q === "" || `${a.title} ${a.description}`.toLowerCase().includes(q.toLowerCase())),
  );

  return (
    <div className="mx-auto max-w-5xl p-6">
      <h1 className="text-xl font-semibold">Marketplace</h1>
      <p className="mt-1 text-sm text-[var(--text-secondary)]">
        Deploy a curated app in one click. Each app is a tested kuso.yaml.
      </p>

      <div className="mt-4 flex flex-wrap items-center gap-2">
        <Input placeholder="Search apps…" value={q} onChange={(e) => setQ(e.target.value)} className="max-w-xs" />
        {categories.map((c) => (
          <button
            key={c}
            onClick={() => setCat(c)}
            className={`rounded-full px-3 py-1 text-xs ${
              cat === c ? "bg-[var(--accent)] text-black" : "bg-[var(--surface-2)] text-[var(--text-secondary)]"
            }`}
          >
            {c}
          </button>
        ))}
      </div>

      {isLoading ? (
        <p className="mt-6 text-sm text-[var(--text-tertiary)]">Loading…</p>
      ) : (
        <div className="mt-6 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {filtered.map((a) => (
            <button
              key={a.name}
              onClick={() => setSelected(a)}
              className="flex flex-col items-start rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4 text-left transition hover:border-[var(--accent)]"
            >
              <img
                src={`${env.apiBase}/api/marketplace/${encodeURIComponent(a.name)}/icon`}
                alt=""
                className="h-8 w-8"
                onError={(e) => ((e.target as HTMLImageElement).style.visibility = "hidden")}
              />
              <span className="mt-2 font-medium">{a.title}</span>
              <span className="mt-1 text-xs text-[var(--text-secondary)] line-clamp-2">{a.description}</span>
              <span className="mt-2 rounded bg-[var(--surface-2)] px-1.5 py-0.5 text-[10px] text-[var(--text-tertiary)]">
                {a.category}
              </span>
            </button>
          ))}
        </div>
      )}

      {selected && <DeployDialog app={selected} onClose={() => setSelected(null)} />}
    </div>
  );
}
```

- [ ] **Step 3: TopNav link**

In `web/src/components/layout/TopNav.tsx`, add a top-level link to `/marketplace` next to the Settings affordance (mirror the existing `<Link href="/settings">` block; label "Marketplace", an appropriate lucide icon e.g. `Store`).

- [ ] **Step 4: Typecheck + build**

Run: `cd web && npx tsc --noEmit && npm run build`
Expected: typecheck clean; static export build succeeds.

Note: `applyConfig` is exported from `@/features/projects`; `createProject` is NOT (it lives un-exported in `mutations.ts`) — the DeployDialog above already creates the project via `api("/api/projects", {method:"POST", body:{name}})` with 409-as-success, so no new export is needed. `env.apiBase` is `""` in the browser, so the icon `<img src>` resolves to the relative `/api/marketplace/<app>/icon` path — correct for the static export.

- [ ] **Step 5: Commit**

```bash
git add web/src/app/\(app\)/marketplace/ web/src/components/marketplace/ web/src/components/layout/TopNav.tsx
git commit -m "feat(web): marketplace catalog page + deploy dialog + nav link"
```

---

## Task 9: Live smoke test + docs

**Files:**
- Modify: `docs/AGENT_SMOKE_TEST.md`

**Interfaces:**
- Consumes: the built CLI (`dist/kuso-darwin-arm64` or rebuilt `/tmp/kuso`), a live test cluster from `agent-target.local.json`.

- [ ] **Step 1: Rebuild artifacts**

Run: `cd /Users/sisle/code/work/kuso && cd web && npm run build && cd .. && (cd server-go && go build ./...) && (cd cli && go build -o /tmp/kuso ./cmd)`
Expected: all build.

- [ ] **Step 2: Add smoke steps to docs/AGENT_SMOKE_TEST.md**

Append a "Marketplace" section:

```markdown
## Marketplace one-click deploy

1. `kuso marketplace list` → lists ≥8 apps.
2. `kuso marketplace deploy uptime-kuma --project mkt-smoke --set host=mkt-smoke.<baseDomain> --dry-run` → prints kuso.yaml with the host substituted, creates nothing.
3. Drop `--dry-run` → project `mkt-smoke` created, service reaches Ready.
4. `curl -I https://mkt-smoke.<baseDomain>` → 200 (allow a minute for cert + rollout).
5. Cleanup: `kuso get projects -o json` to confirm, then delete `mkt-smoke`.
```

- [ ] **Step 3: Run the smoke test against the live cluster**

Follow the steps above using the target from `agent-target.local.json`. Expected: deploy succeeds, service Ready, HTTP 200, cleanup done.

Note: this step requires a live cluster; if unavailable, mark it and hand back to the user to run — do not fake the result.

- [ ] **Step 4: Commit**

```bash
git add docs/AGENT_SMOKE_TEST.md
git commit -m "docs(smoke): marketplace one-click deploy smoke steps"
```

---

## Self-Review Notes

- **Spec coverage:** catalog embed (T3), manifest+prompt schema (T1), render/substitution (T2), 4 endpoints (T4), 8-app catalog (T3+T5), CLI parity (T6), web page+dialog+unified-nav (T8), unified catalog "Datastores" category — **partially deferred**: the spec's addon-card deep-link is noted in the design as UI convergence; T8 ships the app grid; the addon-card row is a fast follow (add to page.tsx once app grid lands). Flagged here rather than silently dropped. Testing (T3 CI guardrail + T4 handler tests + T9 live smoke) covered. Risks: weekly image-tag CI job is a v1.1 ops task, not code — noted in spec, not a plan task.
- **Placeholder scan:** T2 step 3 intentionally flags a cleanup (`strimble`/`specMarshal`) with explicit instructions — the implementer must land exactly one `specMarshal` calling `yaml.Marshal`. Addon conn-secret key names in T5 (n8n/plausible refs) require live confirmation — flagged inline per template, not left vague.
- **Type consistency:** `Note{Kind,Detail}` used in T2, T4, T6, T7 consistently. `RenderTemplate(m,tmpl,project,answers)` signature identical across T2/T3/T4. `MarshalFile` defined in T2, used in T4. `KusoClient` receiver used in T6. `MarketplaceApp`/`RenderResult` shared T7/T8.
- **Deferred to fast-follow (documented, not dropped):** baseDomain auto-fill for domain-kind prompts (T8 step 1 note); addon-card deep-link row in the grid; weekly upstream-tag CI check.
