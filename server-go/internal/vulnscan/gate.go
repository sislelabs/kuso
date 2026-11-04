package vulnscan

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"kuso/server/internal/builds"
	"kuso/server/internal/db"
)

const (
	settingsTTL = 30 * time.Second
	// failSkew tolerates server/DB clock skew when deciding whether a
	// failed scan was requested by this gate (createdAt is DB time,
	// Since is server time).
	failSkew = time.Minute
)

// DigestResolver maps an image reference to its manifest digest; "" with
// a nil error means "can't know up front" and the scan is keyed by ref.
type DigestResolver interface {
	ResolveDigest(ctx context.Context, imageRef string) (string, error)
}

// Gate implements builds.VulnGate on top of the scan queue. Check only
// reads and enqueues; the leader's Worker does the scanning.
type Gate struct {
	DB      *db.DB
	Digests DigestResolver
	Logger  *slog.Logger
	Now     func() time.Time

	mu         sync.Mutex
	settings   db.VulnScanSettings
	settingsAt time.Time
	digests    map[string]string // image ref → digest; build tags are immutable
}

var _ builds.VulnGate = (*Gate)(nil)

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Gate) loadSettings(ctx context.Context) (db.VulnScanSettings, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.settingsAt.IsZero() && g.now().Sub(g.settingsAt) < settingsTTL {
		return g.settings, nil
	}
	st, err := g.DB.GetVulnScanSettings(ctx)
	if err != nil {
		return st, err
	}
	g.settings, g.settingsAt = st, g.now()
	return st, nil
}

// InvalidateSettings drops the cached settings after an admin edit.
func (g *Gate) InvalidateSettings() {
	g.mu.Lock()
	g.settingsAt = time.Time{}
	g.mu.Unlock()
}

func (g *Gate) digest(ctx context.Context, ref string) (string, error) {
	g.mu.Lock()
	d, ok := g.digests[ref]
	g.mu.Unlock()
	if ok || g.Digests == nil {
		return d, nil
	}
	d, err := g.Digests.ResolveDigest(ctx, ref)
	if err != nil || d == "" {
		return d, err
	}
	g.mu.Lock()
	if g.digests == nil || len(g.digests) > 1000 {
		g.digests = map[string]string{}
	}
	g.digests[ref] = d
	g.mu.Unlock()
	return d, nil
}

// Check implements builds.VulnGate.
func (g *Gate) Check(ctx context.Context, req builds.VulnGateRequest) (builds.VulnGateVerdict, error) {
	pass := builds.VulnGateVerdict{Decision: builds.VulnPass}
	st, err := g.loadSettings(ctx)
	if err != nil {
		return pass, err
	}
	if st.Mode == ModeOff {
		return pass, nil
	}
	policy, err := g.DB.GetVulnPolicy(ctx, req.Project, req.Service)
	if err != nil {
		return pass, err
	}
	mode := EffectiveMode(st.Mode, policy)
	if mode == ModeOff {
		return pass, nil
	}
	digest, err := g.digest(ctx, req.Image)
	if err != nil {
		return pass, fmt.Errorf("resolve image digest: %w", err)
	}
	ref := req.Image
	if digest != "" {
		ref = ImageRepo(req.Image) + "@" + digest
	}
	scan, err := g.DB.LatestVulnScanForImage(ctx, digest, ref)
	if err != nil {
		return pass, err
	}
	now := g.now()
	switch {
	case scan != nil && scan.Status == db.VulnStatusSucceeded && now.Sub(scan.FinishedAt) < ReuseWindow:
		return g.verdict(ctx, mode, req, *scan)
	case scan != nil && (scan.Status == db.VulnStatusQueued || scan.Status == db.VulnStatusRunning):
		if mode == ModeReport {
			return pass, nil
		}
		return builds.VulnGateVerdict{Decision: builds.VulnPending}, nil
	case scan != nil && scan.Status == db.VulnStatusFailed && !scan.CreatedAt.Before(req.Since.Add(-failSkew)):
		return builds.VulnGateVerdict{Decision: builds.VulnFailOpen, Message: "scan failed: " + scan.Error}, nil
	}
	if _, _, err := g.DB.EnqueueVulnScan(ctx, db.VulnScanRequest{Digest: digest, ImageRef: ref, Reason: db.VulnReasonGate}); err != nil {
		return pass, err
	}
	// Report mode records the scan but never makes a deploy wait for it.
	if mode == ModeReport {
		return pass, nil
	}
	return builds.VulnGateVerdict{Decision: builds.VulnPending}, nil
}

func (g *Gate) verdict(ctx context.Context, mode string, req builds.VulnGateRequest, scan db.VulnScan) (builds.VulnGateVerdict, error) {
	pass := builds.VulnGateVerdict{Decision: builds.VulnPass, Scanned: true}
	if mode != ModeEnforce || scan.Counts.FixableCritical == 0 {
		return pass, nil
	}
	findings, err := g.DB.ListVulnFindings(ctx, scan.ID)
	if err != nil {
		return pass, err
	}
	ignores, err := g.DB.ListVulnIgnores(ctx, db.VulnOwnerService, req.Project, req.Service)
	if err != nil {
		return pass, err
	}
	ids := Blocking(mode, findings, ignores, g.now())
	if len(ids) == 0 {
		return pass, nil
	}
	return builds.VulnGateVerdict{Decision: builds.VulnBlock, Message: BlockMessage(req.Project, req.Service, ids)}, nil
}

// manifestAccept asks for whatever was pushed, index or single
// manifest, so the digest matches what nodes record in imageID.
const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// RegistryDigests resolves digests on the in-cluster registry (plain
// HTTP, anonymous). Images elsewhere resolve to "" and are scanned by
// reference; the report supplies their digest.
type RegistryDigests struct{ HTTP *http.Client }

func (r RegistryDigests) ResolveDigest(ctx context.Context, imageRef string) (string, error) {
	if ImageHost(imageRef) != builds.RegistryHost {
		return "", nil
	}
	repo := strings.TrimPrefix(ImageRepo(imageRef), builds.RegistryHost+"/")
	ref := strings.TrimPrefix(imageRef, builds.RegistryHost+"/"+repo)
	ref = strings.TrimLeft(ref, ":@")
	if ref == "" {
		ref = "latest"
	}
	c := r.HTTP
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "http://"+builds.RegistryHost+"/v2/"+repo+"/manifests/"+ref, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", manifestAccept)
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry HEAD %s: status %d", imageRef, resp.StatusCode)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", fmt.Errorf("registry HEAD %s: no Docker-Content-Digest", imageRef)
	}
	return d, nil
}
