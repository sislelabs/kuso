// Package compose converts a docker-compose.yml into kuso's
// config-as-code shape (the kuso.yaml that POST
// /api/projects/{p}/apply consumes). The conversion is a pure
// function from compose bytes to a kuso.yaml document plus a Report
// of every mapping decision and every compose key that has no kuso
// equivalent — flagged, never silently dropped.
//
// Layout (mirrors the coolify importer module):
//   - parse.go    — load + parse docker-compose.yml via compose-go
//   - convert.go  — compose model → kuso.yaml (Doc) + Report
//   - classify.go — datastore-image detection → addon kind + version
//   - report.go   — Report type + rendering
//   - mapping.go  — small shared helpers (slugify, port parse, tag→version)
//
// The module deliberately carries its own kuso.yaml struct (Doc /
// Service / Addon) rather than importing server-go's internal spec
// package — internal/ is not importable across modules, and keeping a
// local mirror is exactly how the coolify importer stays decoupled.
// The authoritative validation still happens server-side when the
// generated YAML is fed back through spec.Parse on apply.
package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"gopkg.in/yaml.v3"
)

// Parse loads and resolves a docker-compose file from raw bytes.
// workingDir anchors relative env_file / build-context paths; pass the
// directory the compose file lives in (or "" when those don't need to
// resolve, e.g. an uploaded file with no sibling env_files).
//
// Interpolation of ${VAR} is disabled: an import shouldn't depend on
// the importer's shell environment, and unresolved variables are
// preserved verbatim so the user can see and fill them in on the kuso
// side rather than having them silently blanked.
func Parse(ctx context.Context, raw []byte, workingDir string) (*types.Project, error) {
	return parse(ctx, raw, workingDir, false)
}

// ParseUploaded parses a compose file received over the network (the
// server's /api/import/compose). Unlike Parse it must never touch the
// local filesystem: `include:` and `extends: {file: …}` are refused
// outright, and the loader runs against an empty temp dir so nothing
// relative resolves to a real file either.
func ParseUploaded(ctx context.Context, raw []byte) (*types.Project, error) {
	if err := rejectFileReferences(raw); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "kuso-compose-")
	if err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	defer os.RemoveAll(dir)
	return parse(ctx, raw, dir, true)
}

// rejectFileReferences refuses the compose keys that make the loader
// read other files. Decoding through yaml.v3 resolves anchors and merge
// keys, so an aliased `extends` mapping is caught too.
func rejectFileReferences(raw []byte) error {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse compose: %w", err)
	}
	if _, ok := doc["include"]; ok {
		return fmt.Errorf("parse compose: `include:` is not supported for uploaded compose files — inline the included services")
	}
	services, _ := doc["services"].(map[string]any)
	for name, s := range services {
		svc, _ := s.(map[string]any)
		ext, _ := svc["extends"].(map[string]any)
		if _, ok := ext["file"]; ok {
			return fmt.Errorf("parse compose: service %q: `extends.file` is not supported for uploaded compose files — inline the base service", name)
		}
	}
	return nil
}

func parse(ctx context.Context, raw []byte, workingDir string, skipInclude bool) (*types.Project, error) {
	if workingDir == "" {
		workingDir = "."
	}
	abs, err := filepath.Abs(workingDir)
	if err != nil {
		abs = workingDir
	}
	cfg := types.ConfigDetails{
		WorkingDir: abs,
		ConfigFiles: []types.ConfigFile{
			{Filename: filepath.Join(abs, "docker-compose.yml"), Content: raw},
		},
	}
	proj, err := loader.LoadWithContext(ctx, cfg, func(o *loader.Options) {
		o.SkipInterpolation = true
		o.SkipValidation = false
		o.SkipNormalization = false
		o.ResolvePaths = false
		o.SkipConsistencyCheck = true
		o.SkipInclude = skipInclude
		// Don't read env_file contents off disk: an import shouldn't
		// fail just because a referenced .env isn't present next to the
		// compose file (and we don't inline those values anyway — we
		// flag them). The EnvFiles field is still populated so the
		// converter can report them.
		o.SkipResolveEnvironment = true
		o.SetProjectName("import", false)
	})
	if err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	return proj, nil
}
