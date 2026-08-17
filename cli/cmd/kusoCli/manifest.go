package kusoCli

import (
	"fmt"
	"os"
	"path/filepath"
)

// The config-as-code manifest historically went by two names: `kuso
// init` writes kuso.yml, while `apply` defaulted to kuso.yaml and
// zero-arg `status` read only kuso.yml — so an init'd repo and a
// hand-written one each worked with a different half of the CLI.
// resolveManifestPath is the single resolver every manifest-reading
// command goes through.
const (
	manifestNamePrimary = "kuso.yml" // the name `kuso init` writes
	manifestNameAlt     = "kuso.yaml"
)

// resolveManifestPath finds the manifest in dir, accepting both
// kuso.yml and kuso.yaml. When both exist it prefers kuso.yml (matching
// `kuso init`) and returns a non-empty note naming the ignored file so
// callers can surface it on stderr. When neither exists the error names
// both accepted spellings.
func resolveManifestPath(dir string) (path string, note string, err error) {
	yml := filepath.Join(dir, manifestNamePrimary)
	yaml := filepath.Join(dir, manifestNameAlt)
	ymlOK := isRegularFile(yml)
	yamlOK := isRegularFile(yaml)
	switch {
	case ymlOK && yamlOK:
		return yml, fmt.Sprintf("both %s and %s exist; using %s (the name `kuso init` writes) — remove or merge %s to avoid drift",
			manifestNamePrimary, manifestNameAlt, manifestNamePrimary, manifestNameAlt), nil
	case ymlOK:
		return yml, "", nil
	case yamlOK:
		return yaml, "", nil
	default:
		return "", "", fmt.Errorf("no %s (or %s) found in %s — run `kuso init` or pass a path explicitly",
			manifestNamePrimary, manifestNameAlt, dir)
	}
}

// isRegularFile reports whether path exists and is a regular file (a
// directory named kuso.yml is not a manifest).
func isRegularFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
