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
