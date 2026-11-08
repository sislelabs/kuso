package builds

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"kuso/server/internal/kube"
)

// EnvExampleMaxBytes caps the .env.example a service can import; anything
// larger is not an example file.
const EnvExampleMaxBytes = 64 << 10

// EnvExampleMaxEntries caps how many keys one import returns.
const EnvExampleMaxEntries = 500

// envExampleNames are the file names tried, in order, in the service's
// build directory and then the repo root.
var envExampleNames = []string{".env.example", ".env.sample"}

// ErrRepoUnreachable marks a git host that answered with an error other
// than "no such file" (rate limit, outage, auth).
var ErrRepoUnreachable = errors.New("builds: repository unreachable")

// RepoFileReader reads one file at a branch through the GitHub App.
// ("", nil) means the file doesn't exist. *github.Client implements it.
type RepoFileReader interface {
	ReadFile(ctx context.Context, installationID int64, owner, repo, branch, path string) (string, error)
}

// PublicRepoFileReader reads one file from a repo without an App
// installation (public repos). ("", nil) means not found. Implemented by
// *gitremote.Inspector.
type PublicRepoFileReader interface {
	ReadFile(ctx context.Context, repoURL, branch, path, token string) (string, error)
}

// EnvExampleEntry is one KEY=value line of an example file. Comment is the
// "# …" block directly above the key, if any.
type EnvExampleEntry struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

// EnvExample is what ReadEnvExample found. File is empty when no
// candidate exists; Checked lists every path tried, in order.
type EnvExample struct {
	File      string            `json:"file"`
	Branch    string            `json:"branch"`
	Checked   []string          `json:"checked"`
	Entries   []EnvExampleEntry `json:"entries"`
	Truncated bool              `json:"truncated,omitempty"`
}

// envExampleCandidates lists the paths to try: the service's build
// directory first (when it isn't the root), then the repo root.
func envExampleCandidates(repoPath string) []string {
	dir := strings.Trim(path.Clean("/"+strings.TrimSpace(repoPath)), "/")
	var out []string
	if dir != "" {
		for _, n := range envExampleNames {
			out = append(out, dir+"/"+n)
		}
	}
	return append(out, envExampleNames...)
}

func (s *Service) repoFileReader() RepoFileReader {
	if r, ok := s.RepoAccess.(RepoFileReader); ok {
		return r
	}
	if r, ok := s.Tokens.(RepoFileReader); ok {
		return r
	}
	return nil
}

// ReadEnvExample reads the service's .env.example (or .env.sample) from
// its repo at the deploy branch and returns the keys with their example
// values. It never writes anything.
func (s *Service) ReadEnvExample(ctx context.Context, project, service string) (*EnvExample, error) {
	ns := s.nsFor(ctx, project)
	svcCR, err := s.Kube.GetOwnedService(ctx, ns, project, project+"-"+service)
	if kube.IsNotFoundOrNotOwned(err) {
		return nil, fmt.Errorf("%w: service %s/%s", ErrNotFound, project, service)
	}
	if err != nil {
		return nil, fmt.Errorf("get service: %w", err)
	}
	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	repoURL, repoPath := "", ""
	if svcCR.Spec.Repo != nil {
		repoURL, repoPath = svcCR.Spec.Repo.URL, svcCR.Spec.Repo.Path
	}
	if repoURL == "" && proj.Spec.DefaultRepo != nil {
		repoURL = proj.Spec.DefaultRepo.URL
	}
	if repoURL == "" || svcCR.Spec.Runtime == "image" {
		return nil, fmt.Errorf("%w: this service doesn't build from a git repo", ErrInvalid)
	}
	if err := ValidateRepoURL(repoURL); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	repoURL = NormalizeRepoURL(repoURL)
	branch := effectiveDefaultBranch(proj, svcCR)
	if err := ValidateGitRef(branch); err != nil {
		return nil, fmt.Errorf("%w: branch: %s", ErrInvalid, err.Error())
	}
	owner, repo := splitGithubURL(repoURL)
	if owner == "" {
		return nil, fmt.Errorf("%w: importing .env.example works for github.com repos only", ErrInvalid)
	}

	installationID := githubInstallationID(proj, svcCR)
	if installationID == 0 && s.InstallResolver != nil {
		if id, rerr := s.InstallResolver.ResolveInstallationForRepo(ctx, owner, repo); rerr == nil && id > 0 {
			installationID = id
		}
	}
	read := func(ctx context.Context, p string) (string, error) {
		if r := s.repoFileReader(); installationID > 0 && r != nil {
			return r.ReadFile(ctx, installationID, owner, repo, branch, p)
		}
		pub, ok := s.Remote.(PublicRepoFileReader)
		if !ok {
			return "", fmt.Errorf("%w: no way to read the repository is configured", ErrRepoUnreachable)
		}
		return pub.ReadFile(ctx, repoURL, branch, p, "")
	}

	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out := &EnvExample{Branch: branch, Entries: []EnvExampleEntry{}}
	for _, p := range envExampleCandidates(repoPath) {
		out.Checked = append(out.Checked, p)
		body, err := read(rctx, p)
		if err != nil {
			return nil, fmt.Errorf("%w: read %s: %v", ErrRepoUnreachable, p, err)
		}
		if body == "" {
			continue
		}
		if len(body) > EnvExampleMaxBytes {
			return nil, fmt.Errorf("%w: %s is larger than %d KB", ErrInvalid, p, EnvExampleMaxBytes>>10)
		}
		out.File = p
		out.Entries, out.Truncated = ParseEnvExample(body, EnvExampleMaxEntries)
		break
	}
	return out, nil
}

var envExampleKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseEnvExample reads KEY=value lines the way the Variables panel's raw
// editor does: optional "export ", # comments, single quotes literal,
// double quotes with \n \t \" \\ escapes, unquoted values lose an inline
// " # comment". Malformed lines and repeated keys are skipped (an example
// file is a hint, not a contract). Multi-line quoted values are not
// supported; such lines are skipped. Returns at most max entries and
// whether more were dropped.
func ParseEnvExample(body string, max int) ([]EnvExampleEntry, bool) {
	out := []EnvExampleEntry{}
	seen := map[string]bool{}
	var comment []string
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			comment = nil
			continue
		}
		if strings.HasPrefix(t, "#") {
			if c := strings.TrimSpace(strings.TrimLeft(t, "#")); c != "" {
				comment = append(comment, c)
			}
			continue
		}
		c := strings.Join(comment, " ")
		comment = nil
		t = strings.TrimSpace(strings.TrimPrefix(t, "export "))
		eq := strings.IndexByte(t, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(t[:eq])
		if !envExampleKeyRE.MatchString(key) || seen[key] {
			continue
		}
		value, ok := envExampleValue(strings.TrimLeft(t[eq+1:], " \t"))
		if !ok {
			continue
		}
		if len(out) >= max {
			return out, true
		}
		seen[key] = true
		if len(c) > 200 {
			c = c[:200]
		}
		out = append(out, EnvExampleEntry{Key: key, Value: value, Comment: c})
	}
	return out, false
}

func envExampleValue(v string) (string, bool) {
	if v == "" {
		return "", true
	}
	switch q := v[0]; q {
	case '\'':
		end := strings.IndexByte(v[1:], '\'')
		if end < 0 {
			return "", false
		}
		return v[1 : end+1], true
	case '"':
		var b strings.Builder
		for i := 1; i < len(v); i++ {
			ch := v[i]
			if ch == '\\' && i+1 < len(v) {
				i++
				switch v[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				default:
					b.WriteByte(v[i])
				}
				continue
			}
			if ch == '"' {
				return b.String(), true
			}
			b.WriteByte(ch)
		}
		return "", false
	}
	// Unquoted: an inline comment needs whitespace before the # so URL
	// fragments survive.
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			v = v[:i]
			break
		}
	}
	return strings.TrimSpace(v), true
}
