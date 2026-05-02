package projects

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// VarRef parses a `${{ <addon>.<KEY> }}` reference. The whole-string
// form rewrites the env var into a valueFrom: secretKeyRef pointing at
// the addon's connection-secret. Composite forms (anything around the
// reference) are rejected because we want zero runtime templating —
// kube does the resolution via envFrom/valueFrom.

// ErrCompositeVarRef is returned when an env var value mixes literals
// with a `${{ ... }}` reference. The user must pick one or the other.
var ErrCompositeVarRef = errors.New("variable references must be the entire value")

// VarRef is the parsed shape of a pure `${{ <addon>.<KEY> }}` value.
type VarRef struct {
	Addon string
	Key   string
}

// SecretName returns the kube Secret name where the connection lives.
// Convention: `<addon-cr-name>-conn`. Mirrors the helm chart's naming
// (see addonchart.tpl). Phase 7 of the v0.1 redesign reduced this to
// release-name-plus-suffix; we keep it consistent here.
func (r VarRef) SecretName() string {
	return r.Addon + "-conn"
}

// pure-form regex: ^\$\{\{\s*<addon>\.<KEY>\s*\}\}$
var varRefRe = regexp.MustCompile(`^\$\{\{\s*([a-zA-Z0-9_-]+)\.([A-Z_][A-Z0-9_]*)\s*\}\}$`)

// composite-form regex: any `${{ ... }}` inside but with surrounding
// content. Used to distinguish "valid pure ref" from "user typo".
var anyVarRefRe = regexp.MustCompile(`\$\{\{[^}]*\}\}`)

// ParseVarRef returns (ref, true, nil) for a pure reference,
// (zero, false, nil) for a literal value with no reference at all,
// and (zero, false, ErrCompositeVarRef) for any composite form.
func ParseVarRef(value string) (VarRef, bool, error) {
	m := varRefRe.FindStringSubmatch(value)
	if m != nil {
		return VarRef{Addon: m[1], Key: m[2]}, true, nil
	}
	if anyVarRefRe.MatchString(value) {
		return VarRef{}, false, ErrCompositeVarRef
	}
	return VarRef{}, false, nil
}

// RewriteEnvVar maps a wire-shape EnvVar through the var-ref parser. If
// the value is a pure ref, it's converted to a valueFrom.secretKeyRef.
// If the value is a literal, it's returned unchanged. Composite refs
// return ErrCompositeVarRef wrapped with the offending var name.
func RewriteEnvVar(in EnvVar) (EnvVar, error) {
	// Pre-existing valueFrom entries pass through. Only literal `value`
	// entries are candidates for rewriting.
	if in.ValueFrom != nil || in.Value == "" {
		return in, nil
	}
	ref, ok, err := ParseVarRef(in.Value)
	if err != nil {
		return EnvVar{}, fmt.Errorf("env var %q: %w", in.Name, err)
	}
	if !ok {
		return in, nil
	}
	return EnvVar{
		Name: in.Name,
		ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{
				"name": ref.SecretName(),
				"key":  ref.Key,
			},
		},
	}, nil
}

// RewriteEnvVars applies RewriteEnvVar over a slice. Returns the first
// error encountered, or the rewritten slice.
func RewriteEnvVars(in []EnvVar) ([]EnvVar, error) {
	out := make([]EnvVar, 0, len(in))
	for _, v := range in {
		rewritten, err := RewriteEnvVar(v)
		if err != nil {
			return nil, err
		}
		out = append(out, rewritten)
	}
	return out, nil
}

// FormatVarRef returns the canonical wire form for a (addon, key) pair.
// Used by the frontend autocomplete to insert the right syntax.
func FormatVarRef(addon, key string) string {
	return "${{ " + strings.TrimSpace(addon) + "." + strings.TrimSpace(key) + " }}"
}
