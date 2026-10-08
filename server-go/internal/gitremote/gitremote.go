// Package gitremote reads a git repository's branches and head commits
// over the smart-HTTP protocol, with no GitHub App. It works against any
// git host and takes an optional access token for private repos.
package gitremote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"kuso/server/internal/httpx"
)

var (
	// ErrNotAccessible: the host answered 401/403/404. For a private repo
	// that means "add a token"; git hosts deliberately don't distinguish
	// missing from forbidden.
	ErrNotAccessible = errors.New("repository not found or not accessible")
	// ErrBranchNotFound: the repo is reachable but has no such branch.
	ErrBranchNotFound = errors.New("branch not found")
	// ErrUnsupported: the URL isn't one this package can talk to (ssh).
	ErrUnsupported = errors.New("unsupported repository URL")
)

// maxAdvertisement bounds the ref listing we'll read; a repo with tens of
// thousands of refs is truncated rather than buffered whole.
const maxAdvertisement = 8 << 20

type Branch struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

type Info struct {
	DefaultBranch string   `json:"defaultBranch"`
	Branches      []Branch `json:"branches"`
}

// Inspector talks to git hosts. The zero value uses an SSRF-safe client;
// tests inject HTTP to reach a loopback server.
type Inspector struct {
	HTTP *http.Client
	// GitHubAPI overrides https://api.github.com (tests).
	GitHubAPI string
}

func (in *Inspector) client() *http.Client {
	if in != nil && in.HTTP != nil {
		return in.HTTP
	}
	return httpx.SSRFSafeClient(15 * time.Second)
}

// Refs lists the repo's branches and its default branch.
func (in *Inspector) Refs(ctx context.Context, repoURL, token string) (*Info, error) {
	base, err := httpBase(repoURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, repoURL)
	}
	req.Header.Set("User-Agent", "git/kuso")
	if token != "" {
		req.SetBasicAuth(tokenUser(repoURL), token)
	}
	resp, err := in.client().Do(req)
	if err != nil {
		// *url.Error embeds the request URL, never the Authorization header.
		return nil, fmt.Errorf("reach %s: %w", base, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s (HTTP %d)", ErrNotAccessible, base, resp.StatusCode)
	default:
		return nil, fmt.Errorf("list refs of %s: HTTP %d", base, resp.StatusCode)
	}
	return parseAdvertisement(io.LimitReader(resp.Body, maxAdvertisement))
}

// HeadSHA returns the commit a branch points at.
func (in *Inspector) HeadSHA(ctx context.Context, repoURL, branch, token string) (string, error) {
	info, err := in.Refs(ctx, repoURL, token)
	if err != nil {
		return "", err
	}
	for _, b := range info.Branches {
		if b.Name == branch {
			return b.SHA, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrBranchNotFound, branch)
}

// httpBase normalises a repo URL to the smart-HTTP base (always ".git").
func httpBase(repoURL string) (string, error) {
	u := strings.TrimSpace(repoURL)
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return "", fmt.Errorf("%w: %s (only http/https)", ErrUnsupported, repoURL)
	}
	u = strings.TrimRight(u, "/")
	if !strings.HasSuffix(u, ".git") {
		u += ".git"
	}
	return u, nil
}

// tokenUser is the basic-auth username each host expects next to a token.
// GitLab wants "oauth2"; GitHub (and Gitea) accept any name.
func tokenUser(repoURL string) string {
	if strings.Contains(strings.ToLower(repoURL), "gitlab") {
		return "oauth2"
	}
	return "x-access-token"
}

// parseAdvertisement reads a v0 upload-pack ref advertisement: pkt-lines
// of "<sha> <ref>", the first carrying NUL-separated capabilities that
// include "symref=HEAD:refs/heads/<default>".
func parseAdvertisement(r io.Reader) (*Info, error) {
	br := bufio.NewReader(r)
	info := &Info{}
	headSHA := ""
	for {
		line, err := readPkt(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue // flush packet or the "# service=" banner
		}
		caps := ""
		if i := strings.IndexByte(line, 0); i >= 0 {
			line, caps = line[:i], line[i+1:]
		}
		sha, ref, ok := strings.Cut(strings.TrimRight(line, "\n"), " ")
		if !ok || len(sha) != 40 {
			continue
		}
		for _, c := range strings.Fields(caps) {
			if target, found := strings.CutPrefix(c, "symref=HEAD:refs/heads/"); found {
				info.DefaultBranch = target
			}
		}
		switch {
		case ref == "HEAD":
			headSHA = sha
		case strings.HasPrefix(ref, "refs/heads/"):
			info.Branches = append(info.Branches, Branch{Name: strings.TrimPrefix(ref, "refs/heads/"), SHA: sha})
		}
	}
	sort.Slice(info.Branches, func(i, j int) bool { return info.Branches[i].Name < info.Branches[j].Name })
	if info.DefaultBranch == "" {
		// Old servers don't advertise symref: fall back to the branch HEAD
		// points at, preferring main/master on a tie.
		for _, want := range []string{"main", "master", ""} {
			for _, b := range info.Branches {
				if b.SHA == headSHA && (want == "" || b.Name == want) {
					info.DefaultBranch = b.Name
					break
				}
			}
			if info.DefaultBranch != "" {
				break
			}
		}
	}
	return info, nil
}

// readPkt returns one pkt-line payload; "" for a flush packet.
func readPkt(br *bufio.Reader) (string, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return "", io.EOF
		}
		return "", err
	}
	n, err := strconv.ParseUint(string(hdr[:]), 16, 16)
	if err != nil {
		return "", fmt.Errorf("not a git smart-HTTP response")
	}
	if n < 4 {
		return "", nil
	}
	buf := make([]byte, n-4)
	if _, err := io.ReadFull(br, buf); err != nil {
		return "", io.EOF // truncated by the size cap: keep what we have
	}
	return string(buf), nil
}
