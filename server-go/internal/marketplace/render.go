package marketplace

import (
	"errors"
	"fmt"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"

	"kuso/server/internal/spec"
)

// ErrRender is returned when a template can't be rendered (bad answers,
// unknown token, invalid resulting spec).
var ErrRender = errors.New("marketplace: render")

// Note is one human-readable line describing what a render produced,
// shown in the UI/CLI before apply.
type Note struct {
	Kind   string `json:"kind"` // service | addon | secret | domain | info
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
