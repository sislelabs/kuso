package crons

import (
	"context"
	"fmt"
	"path"
	"strings"

	"kuso/server/internal/kube"
)

// RepoFileFetcher reads one file from a git host at an exact commit.
// Implemented over the GitHub App client by the HTTP layer (the github
// package imports crons, so crons can't import it back).
type RepoFileFetcher interface {
	GetRepoFile(ctx context.Context, installationID int64, repoURL, ref, filePath string) (content []byte, found bool, err error)
	BlobURL(repoURL, ref, filePath string) string
}

// CronSource is the wire shape of GET .../crons/{name}/source. When
// Available is false, Reason says why in words the UI can show as is.
type CronSource struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Path      string `json:"path,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Content   string `json:"content,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	HTMLURL   string `json:"htmlUrl,omitempty"`
}

// maxSourceBytes caps the body we ship to the browser; cron entrypoints
// are small, and a generated bundle shouldn't stall the panel.
const maxSourceBytes = 256 << 10

// maxSourceCandidates bounds GitHub API calls per request.
const maxSourceCandidates = 3

var sourceExts = map[string]bool{
	".ts": true, ".tsx": true, ".mts": true, ".cts": true,
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".py": true, ".rb": true, ".go": true, ".rs": true, ".php": true,
	".sh": true, ".bash": true, ".ex": true, ".exs": true,
	".java": true, ".kt": true, ".scala": true, ".cs": true,
	".sql": true, ".pl": true, ".lua": true, ".dart": true, ".swift": true,
}

// sourceCandidates picks the argv tokens that look like a repo source
// file. A `sh -c "..."` string is split on whitespace so its script path
// is found too. Vendored (node_modules) and absolute paths are skipped:
// they are never the user's code in the repo.
func sourceCandidates(command []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, arg := range command {
		for _, tok := range strings.Fields(arg) {
			tok = strings.Trim(tok, `"'`)
			if tok == "" || strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "/") || strings.Contains(tok, "://") {
				continue
			}
			if i := strings.IndexByte(tok, '='); i >= 0 {
				continue // --flag=value / KEY=value
			}
			p := path.Clean(tok)
			if strings.HasPrefix(p, "../") || p == ".." || strings.HasPrefix(p, "node_modules/") || strings.Contains(p, "/node_modules/") {
				continue
			}
			if !sourceExts[strings.ToLower(path.Ext(p))] || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// Source returns the repo file a service cron runs, read at the exact
// commit its image was built from. The commit comes from the KusoBuild
// that produced spec.image.tag, so it can't drift to an undeployed push.
func (s *Service) Source(ctx context.Context, project, service, name string, files RepoFileFetcher) (*CronSource, error) {
	c, err := s.Get(ctx, project, service, name)
	if err != nil {
		return nil, err
	}
	if c.Spec.Kind != "" && c.Spec.Kind != "service" {
		return &CronSource{Reason: "only service crons run code from the repo"}, nil
	}
	candidates := sourceCandidates(c.Spec.Command)
	if len(candidates) == 0 {
		return &CronSource{Reason: "the command doesn't name a source file"}, nil
	}
	if c.Spec.Image == nil || c.Spec.Image.Tag == "" {
		return &CronSource{Reason: "the cron has no image yet"}, nil
	}
	if files == nil {
		return &CronSource{Reason: "the GitHub App isn't configured"}, nil
	}

	fqn := c.Spec.Service
	if fqn == "" {
		fqn = project + "-" + strings.TrimPrefix(service, project+"-")
	}
	b, err := s.buildForImage(ctx, project, fqn, c.Spec.Image)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return &CronSource{Reason: "no build found for image " + c.Spec.Image.Tag}, nil
	}
	if b.Spec.Repo == nil || b.Spec.Repo.URL == "" || b.Spec.GithubInstallationID <= 0 {
		return &CronSource{Reason: "the service isn't built from a GitHub repo"}, nil
	}

	root := strings.Trim(path.Clean("/"+b.Spec.Repo.Path), "/")
	for i, cand := range candidates {
		if i == maxSourceCandidates {
			break
		}
		repoPath := cand
		if root != "" {
			repoPath = root + "/" + cand
		}
		body, found, err := files.GetRepoFile(ctx, b.Spec.GithubInstallationID, b.Spec.Repo.URL, b.Spec.Ref, repoPath)
		if err != nil {
			return nil, fmt.Errorf("read %s@%s: %w", repoPath, b.Spec.Ref, err)
		}
		if !found {
			continue
		}
		out := &CronSource{
			Available: true,
			Path:      repoPath,
			Commit:    b.Spec.Ref,
			HTMLURL:   files.BlobURL(b.Spec.Repo.URL, b.Spec.Ref, repoPath),
		}
		if len(body) > maxSourceBytes {
			body = body[:maxSourceBytes]
			out.Truncated = true
		}
		out.Content = string(body)
		return out, nil
	}
	return &CronSource{
		Commit: b.Spec.Ref,
		Reason: fmt.Sprintf("%s isn't in the repo at %.12s (a build output?)", candidates[0], b.Spec.Ref),
	}, nil
}

// buildForImage finds the KusoBuild of service fqn that produced img.
func (s *Service) buildForImage(ctx context.Context, project, fqn string, img *kube.KusoImage) (*kube.KusoBuild, error) {
	all, err := s.Kube.ListKusoBuildsByLabels(ctx, s.nsFor(ctx, project), map[string]string{"kuso.sislelabs.com/project": project})
	if err != nil {
		return nil, fmt.Errorf("list builds: %w", err)
	}
	for i := range all {
		b := &all[i]
		if b.Spec.Service != fqn || b.Spec.DryRun {
			continue
		}
		tag := b.Annotations["kuso.sislelabs.com/image-tag"]
		if b.Spec.Image != nil && b.Spec.Image.Tag != "" {
			tag = b.Spec.Image.Tag
		}
		if tag != img.Tag {
			continue
		}
		if b.Spec.Image != nil && b.Spec.Image.Repository != "" && img.Repository != "" && b.Spec.Image.Repository != img.Repository {
			continue
		}
		return b, nil
	}
	return nil, nil
}
