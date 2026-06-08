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
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
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
