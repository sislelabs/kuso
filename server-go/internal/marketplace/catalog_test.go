package marketplace

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"kuso/server/internal/kube"
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

// connKeys lists the keys each addon kind's <name>-conn Secret exposes
// (operator/helm-charts/kusoaddon/templates/<kind>.yaml). A template ref
// to any other key resolves to a missing secret key and the pod never
// starts, so the catalog test pins refs to this list.
var connKeys = map[string][]string{
	"postgres":   {"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB", "DATABASE_URL", "DIRECT_URL", "POOLER_HOST", "POOLER_PORT", "POOLER_URL"},
	"mysql":      {"MYSQL_HOST", "MYSQL_PORT", "MYSQL_USER", "MYSQL_PASSWORD", "MYSQL_DB", "MYSQL_URL", "DATABASE_URL"},
	"redis":      {"REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD", "REDIS_URL"},
	"valkey":     {"REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD", "REDIS_URL", "VALKEY_HOST", "VALKEY_PORT", "VALKEY_URL"},
	"mongodb":    {"MONGO_HOST", "MONGO_PORT", "MONGO_USER", "MONGO_PASSWORD", "MONGO_DB", "MONGO_URL", "MONGODB_URI", "DATABASE_URL"},
	"clickhouse": {"CLICKHOUSE_URL"},
}

// sampleAnswer fills a prompt the way a user would, so every template is
// rendered with ALL prompts answered (not just the required ones).
func sampleAnswer(slug string, p Prompt) string {
	switch {
	case p.Kind == "domain":
		return slug + ".example.com"
	case p.Kind == "password":
		return "Sample-Passw0rd-123"
	case strings.Contains(p.Key, "email"):
		return "admin@example.com"
	case p.Default != "":
		return p.Default
	default:
		return "sample"
	}
}

// TestCatalog_RenderEveryTemplate renders each app with every prompt
// answered, round-trips the result through the real kuso.yaml parser, and
// checks the wiring the parser can't: addon refs name a declared addon
// and a key its conn Secret really has, and securityContext stays inside
// the server's capability allowlist.
func TestCatalog_RenderEveryTemplate(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(entries) < 45 {
		t.Fatalf("catalog has %d apps, want at least 45", len(entries))
	}
	for _, m := range entries {
		e, err := GetEntry(m.Name)
		if err != nil {
			t.Fatalf("%s: %v", m.Name, err)
		}
		answers := map[string]string{}
		for _, p := range m.Prompts {
			answers[p.Key] = sampleAnswer(m.Name, p)
		}
		f, _, err := RenderTemplate(m, e.TemplateYAML, "smoke-"+m.Name, answers)
		if err != nil {
			t.Errorf("%s: render: %v", m.Name, err)
			continue
		}
		out, err := MarshalFile(f)
		if err != nil {
			t.Errorf("%s: marshal: %v", m.Name, err)
			continue
		}
		parsed, err := spec.Parse(out)
		if err != nil {
			t.Errorf("%s: rendered kuso.yaml does not parse: %v", m.Name, err)
			continue
		}
		if strings.Contains(string(out), "${{ prompt.") {
			t.Errorf("%s: rendered output still has a prompt token", m.Name)
		}
		addonKind := map[string]string{}
		for _, a := range parsed.Addons {
			addonKind[a.Name] = a.Kind
		}
		services := map[string]bool{}
		for _, s := range parsed.Services {
			services[s.Name] = true
		}
		for _, s := range parsed.Services {
			if s.Image == nil || s.Image.Repository == "" {
				t.Errorf("%s/%s: marketplace apps must be image services", m.Name, s.Name)
			}
			if s.Port == 0 {
				t.Errorf("%s/%s: no port", m.Name, s.Name)
			}
			for k, ev := range s.Env {
				// Mirrors projects.envNameReserved: apply rejects these, so
				// the whole service fails to create.
				if k == "PORT" || k == "HOSTNAME" || strings.HasPrefix(k, "KUBERNETES_") {
					t.Errorf("%s/%s: env %s is reserved by kuso", m.Name, s.Name, k)
				}
				ref, ok, _ := projects.ParseVarRef(ev.Value)
				if !ok || ev.Generate != "" {
					continue
				}
				if services[ref.Name] {
					continue
				}
				kind, declared := addonKind[ref.Name]
				if !declared {
					t.Errorf("%s/%s: env %s refs undeclared addon %q", m.Name, s.Name, k, ref.Name)
					continue
				}
				keys, known := connKeys[kind]
				if !known {
					t.Errorf("%s/%s: env %s refs addon kind %q with no known conn keys", m.Name, s.Name, k, kind)
					continue
				}
				if !slices.Contains(keys, ref.Key) {
					t.Errorf("%s/%s: env %s refs %s.%s, but a %s conn Secret has no such key", m.Name, s.Name, k, ref.Name, ref.Key, kind)
				}
			}
			if sc := s.SecurityContext; sc != nil {
				ksc := &kube.KusoSecurityContext{AllowPrivilegeEscalation: sc.AllowPrivilegeEscalation}
				if sc.Capabilities != nil {
					ksc.Capabilities = &kube.KusoCapabilities{Add: sc.Capabilities.Add}
				}
				if err := kube.ValidateSecurityContext(ksc); err != nil {
					t.Errorf("%s/%s: securityContext rejected by the server: %v", m.Name, s.Name, err)
				}
			}
		}
	}
}
