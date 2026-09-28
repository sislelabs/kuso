package projects

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"kuso/server/internal/registrycreds"
)

const maxWatchPaths = 50

// normalizeWatchPaths trims, drops blanks and validates push-build watch
// globs. Patterns are repo-root relative; `..` segments are rejected and
// every non-`**` segment must be valid path.Match syntax.
func normalizeWatchPaths(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if len(p) > 256 {
			return nil, fmt.Errorf("%w: watch path %q is longer than 256 chars", ErrInvalid, p)
		}
		for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
			if seg == ".." {
				return nil, fmt.Errorf("%w: watch path %q must stay inside the repo (no ..)", ErrInvalid, p)
			}
			if seg == "**" {
				continue
			}
			if _, err := path.Match(seg, ""); err != nil {
				return nil, fmt.Errorf("%w: watch path %q is not a valid glob: %v", ErrInvalid, p, err)
			}
		}
		out = append(out, p)
	}
	if len(out) > maxWatchPaths {
		return nil, fmt.Errorf("%w: at most %d watch paths", ErrInvalid, maxWatchPaths)
	}
	return out, nil
}

// resolvePullSecret maps a registry-credential reference to the Secret
// name, verifying it belongs to project (another project's Secret in a
// shared namespace must not be mountable by name).
func (s *Service) resolvePullSecret(ctx context.Context, project, ref string) (string, error) {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return "", err
	}
	name, err := registrycreds.New(s.Kube, ns).Resolve(ctx, project, ref)
	switch {
	case errors.Is(err, registrycreds.ErrNotFound):
		return "", fmt.Errorf("%w: %s", ErrNotFound, strings.TrimPrefix(err.Error(), registrycreds.ErrNotFound.Error()+": "))
	case errors.Is(err, registrycreds.ErrInvalid):
		return "", fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	case err != nil:
		return "", err
	}
	return name, nil
}
