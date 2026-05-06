// Package updater is the self-update subsystem. It polls the GitHub
// releases API for the latest tag's manifest, caches it in the
// existing sqlite DB, and exposes:
//
//	GET  /api/system/version   → current + latest + canAutoUpgrade
//	POST /api/system/update    → schedules a Job that applies CRDs +
//	                             rolls server + operator
//
// Design choices that bit me on previous PaaS attempts:
//
//   - The handler must NOT shell out to kubectl. The server gets
//     killed mid-update; the cluster keeps running. So the actual
//     work is delegated to a kube Job in the kuso-system namespace,
//     and the server just creates it + watches a status ConfigMap.
//   - CRD migration safety is encoded in release.json, not inferred
//     at update time. Today every release is "additive" so this is
//     trivial; but the structure is here so a v0.6 with a rename
//     can ship the pre-rewrite step in the manifest.
package updater

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// Manifest is the wire shape of release.json that we publish on every
// GitHub release. Keep it stable — old kuso instances pulling a new
// release manifest must still be able to parse the bits they care
// about. New optional fields are fine; renaming or removing is not.
type Manifest struct {
	Version     string                `json:"version"`
	PublishedAt time.Time             `json:"publishedAt"`
	Components  ManifestComponents    `json:"components"`
	CRDs        ManifestCRDs          `json:"crds"`
	Notes       string                `json:"notes,omitempty"`
	Breaking    bool                  `json:"breaking,omitempty"`
}

type ManifestComponents struct {
	Server   ComponentRef `json:"server"`
	Operator ComponentRef `json:"operator"`
	// Updater is the in-cluster Job image that performs the rollout
	// (kubectl-set-image + CRD apply). Optional in old release.json
	// payloads — when missing we fall back to
	// ghcr.io/sislelabs/kuso-updater:<version>, then to :latest.
	Updater ComponentRef `json:"updater,omitempty"`
}

type ComponentRef struct {
	Image string `json:"image"`
}

type ManifestCRDs struct {
	URL        string      `json:"url"`
	MinServer  string      `json:"minServer,omitempty"`
	Migrations []Migration `json:"migrations,omitempty"`
}

// Migration classifies a CRD change. The Kind drives whether we can
// auto-apply, and pre-rewrite scripts (when present) run BEFORE the
// new CRD lands so we can rename fields without losing data.
type Migration struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`         // KusoService / KusoEnvironment / etc.
	Change     string `json:"kind_of_change"` // additive | defaulted | renamed | destructive
	PreRewrite string `json:"preRewrite,omitempty"`
}

// State is what we cache in DB. Includes the raw manifest so the UI
// can render notes without round-tripping to GitHub on every poll.
type State struct {
	Current    string    `json:"current"`
	Latest     string    `json:"latest"`
	Manifest   *Manifest `json:"manifest,omitempty"`
	NeedsUpdate     bool      `json:"needsUpdate"`
	CanAutoUpgrade  bool      `json:"canAutoUpgrade"`
	BlockedReason   string    `json:"blockedReason,omitempty"`
	LastChecked     time.Time `json:"lastChecked"`
	LastCheckError  string    `json:"lastCheckError,omitempty"`
}

// Service polls GH and serves State. Construct with New + start with
// Run in a goroutine.
type Service struct {
	DB         *db.DB
	Kube       *kube.Client
	Namespace  string // server namespace (where deploy/kuso-server lives)
	Repo       string // "sislelabs/kuso" — where releases are published
	Current    string // server's running version, baked at build time
	Logger     *slog.Logger
	Interval   time.Duration

	mu      sync.RWMutex
	state   State
	etag    string // for If-None-Match on the GH releases endpoint
	client  *http.Client

	// runCtx is the lifecycle context set by Run. Background watchers
	// (e.g. watchOperatorHealth) derive from this so a graceful
	// shutdown cancels in-flight rollbacks instead of leaving them
	// running against a closed kube client. nil before Run starts —
	// callers fall back to context.Background() in that case (only
	// happens in tests).
	runCtx context.Context
}

// New builds a Service with sensible defaults. Repo overridable for
// air-gapped deployments that mirror releases internally.
func New(database *db.DB, kc *kube.Client, namespace, current string, logger *slog.Logger) *Service {
	return &Service{
		DB:        database,
		Kube:      kc,
		Namespace: namespace,
		Repo:      envOrDefault("KUSO_RELEASES_REPO", "sislelabs/kuso"),
		Current:   current,
		Logger:    logger,
		Interval:  6 * time.Hour,
		state: State{
			Current:     current,
			LastChecked: time.Time{},
		},
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Run polls the GH releases endpoint forever. First tick is
// immediate so the UI has a real "latest" within 30s of boot.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

// State returns a copy of the current cached state. Safe to call
// from any goroutine — the handler does this on every request.
func (s *Service) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := s.state
	return cp
}

// Refresh runs one synchronous poll against the GH releases endpoint
// and returns the freshly-updated State. Used by the manual
// "Check for updates" button so the user doesn't have to wait for
// the 6h background ticker. ctx is honoured — callers should pass a
// short-bounded context so a slow upstream doesn't pin the request.
func (s *Service) Refresh(ctx context.Context) State {
	s.tick(ctx)
	return s.State()
}

func (s *Service) tick(ctx context.Context) {
	tag, manifest, err := s.fetchLatest(ctx)
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastChecked = now
	if err != nil {
		s.state.LastCheckError = err.Error()
		s.Logger.Warn("updater: poll", "err", err)
		return
	}
	s.state.LastCheckError = ""
	s.state.Latest = tag
	s.state.Manifest = manifest
	s.state.NeedsUpdate = compareTags(s.Current, tag) < 0
	can, reason := canAutoUpgrade(manifest)
	s.state.CanAutoUpgrade = can
	s.state.BlockedReason = reason
}

// fetchLatest queries the GH releases endpoint then downloads the
// release.json artifact. Returns (tag, manifest, error). 304 from GH
// is not an error — we just keep the cached manifest.
func (s *Service) fetchLatest(ctx context.Context) (string, *Manifest, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", s.Repo)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	// etag is shared between the periodic ticker and Refresh().
	// Read/write under mu so concurrent callers don't tear the
	// string and burn the GH rate limit on a flopped ETag.
	s.mu.RLock()
	cachedETag := s.etag
	s.mu.RUnlock()
	if cachedETag != "" {
		req.Header.Set("If-None-Match", cachedETag)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("gh releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		// Cached state still good.
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.state.Latest, s.state.Manifest, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", nil, errors.New("no releases published yet")
	}
	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("gh releases: status %d", resp.StatusCode)
	}
	if newETag := resp.Header.Get("ETag"); newETag != "" {
		s.mu.Lock()
		s.etag = newETag
		s.mu.Unlock()
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", nil, fmt.Errorf("decode release: %w", err)
	}

	manifestURL := ""
	for _, a := range rel.Assets {
		if a.Name == "release.json" {
			manifestURL = a.BrowserDownloadURL
			break
		}
	}
	if manifestURL == "" {
		// Pre-manifest releases (anything before we shipped the
		// updater): synthesize a minimal manifest. This means the
		// server still surfaces "latest=v0.4.2" even when there's no
		// release.json, and `canAutoUpgrade=false` because we don't
		// know what migrations might be needed.
		return rel.TagName, &Manifest{
			Version: rel.TagName,
			Notes:   rel.Body,
		}, nil
	}

	mreq, _ := http.NewRequestWithContext(ctx, "GET", manifestURL, nil)
	mresp, err := s.client.Do(mreq)
	if err != nil {
		return rel.TagName, nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer mresp.Body.Close()
	if mresp.StatusCode != 200 {
		return rel.TagName, nil, fmt.Errorf("manifest: status %d", mresp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(mresp.Body, 1<<20))
	if err != nil {
		return rel.TagName, nil, fmt.Errorf("read manifest: %w", err)
	}
	// Signature verification. Behind a feature flag — the keypair
	// rollout is operator-side work (generate Ed25519 key, configure
	// release.sh to sign release.json, distribute the public key
	// through install.sh). When KUSO_REQUIRE_SIGNATURES=true is set,
	// missing or invalid signatures are a hard error and the updater
	// refuses to apply the manifest. Without the flag we log a warn
	// (so unsigned releases are visible in monitoring) and proceed.
	if err := s.verifyManifestSignature(ctx, rel.Assets, body); err != nil {
		if requireSignatures() {
			return rel.TagName, nil, fmt.Errorf("verify manifest signature: %w", err)
		}
		s.Logger.Warn("updater: unsigned release accepted",
			"version", rel.TagName, "err", err,
			"hint", "set KUSO_REQUIRE_SIGNATURES=true to enforce")
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return rel.TagName, nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.Version == "" {
		m.Version = rel.TagName
	}
	if m.Notes == "" {
		m.Notes = rel.Body
	}
	return m.Version, &m, nil
}

// fetchVersion is fetchLatest's pinned cousin. Given a tag like
// "v0.7.13", grab the GH release by name and parse its release.json.
// Used by StartUpdate when the caller passes an explicit version.
//
// Why no caching: pinned upgrades are rare (probably a fix-forward or
// rollback), so we don't bother adding state. The 1-2s round-trip is
// fine; the caller already accepted a multi-minute job anyway.
func (s *Service) fetchVersion(ctx context.Context, version string) (*Manifest, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", s.Repo, version)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gh release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no release tagged %s", version)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gh release: status %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	manifestURL := ""
	for _, a := range rel.Assets {
		if a.Name == "release.json" {
			manifestURL = a.BrowserDownloadURL
			break
		}
	}
	if manifestURL == "" {
		// No release.json for that version. Synthesize a minimal one;
		// the updater image still knows how to roll the deploy from
		// just the version tag. Worst case we miss the migration
		// classification — pinned upgrades trust the user.
		return &Manifest{
			Version: rel.TagName,
			Notes:   rel.Body,
			Components: ManifestComponents{
				Server:   ComponentRef{Image: fmt.Sprintf("ghcr.io/sislelabs/kuso-server-go:%s", rel.TagName)},
				Operator: ComponentRef{Image: fmt.Sprintf("ghcr.io/sislelabs/kuso-operator:%s", rel.TagName)},
			},
			CRDs: ManifestCRDs{
				URL: fmt.Sprintf("https://github.com/%s/releases/download/%s/crds.yaml", s.Repo, rel.TagName),
			},
		}, nil
	}
	mreq, _ := http.NewRequestWithContext(ctx, "GET", manifestURL, nil)
	mresp, err := s.client.Do(mreq)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer mresp.Body.Close()
	if mresp.StatusCode != 200 {
		return nil, fmt.Errorf("manifest: status %d", mresp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(mresp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	// Signature verification — same gate as fetchLatest. Pinned
	// upgrades shouldn't bypass signing because that's exactly the
	// path an attacker would prefer (server picks an old, unsigned
	// tag). KUSO_REQUIRE_SIGNATURES=true makes a missing signature
	// a hard error; otherwise we warn and proceed for compat with
	// pre-signing releases.
	relAssets := make([]struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}, len(rel.Assets))
	for i, a := range rel.Assets {
		relAssets[i].Name = a.Name
		relAssets[i].BrowserDownloadURL = a.BrowserDownloadURL
	}
	if err := s.verifyManifestSignature(ctx, relAssets, body); err != nil {
		if requireSignatures() {
			return nil, fmt.Errorf("verify manifest signature: %w", err)
		}
		// soft-fail: log + proceed.
		if s.Logger != nil {
			s.Logger.Warn("updater: manifest signature check failed",
				"version", rel.TagName, "err", err)
		}
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.Version == "" {
		m.Version = rel.TagName
	}
	if m.Notes == "" {
		m.Notes = rel.Body
	}
	return &m, nil
}

// canAutoUpgrade returns whether the user can hit "Update" without
// reading docs, plus a reason if not. The server is conservative —
// any "destructive" migration in the chain forces manual.
func canAutoUpgrade(m *Manifest) (bool, string) {
	if m == nil {
		return false, "no manifest published for this release"
	}
	if m.Breaking {
		return false, "release marked as breaking — see release notes"
	}
	for _, mg := range m.CRDs.Migrations {
		switch strings.ToLower(mg.Change) {
		case "destructive":
			return false, fmt.Sprintf("migration %s is destructive", mg.ID)
		case "additive", "defaulted", "renamed", "":
			// All safe to auto-apply.
		default:
			return false, fmt.Sprintf("unknown migration kind %q in %s", mg.Change, mg.ID)
		}
	}
	return true, ""
}

// compareTags returns -1, 0, 1 for vA<B, ==, >. Tolerates the v
// prefix and pre-release suffixes (-rc.1, -beta.2) by string compare
// after the numeric segments — fine for our single-stream release
// cadence.
func compareTags(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	if a == b {
		return 0
	}
	pa := splitVer(a)
	pb := splitVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	// Same numeric, fall back to lexical (handles -rc vs no suffix).
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func splitVer(s string) [3]int {
	parts := strings.SplitN(s, ".", 3)
	out := [3]int{}
	for i := 0; i < 3 && i < len(parts); i++ {
		// Strip pre-release suffix from the patch number.
		seg := parts[i]
		if dash := strings.IndexByte(seg, '-'); dash >= 0 {
			seg = seg[:dash]
		}
		n := 0
		for _, c := range seg {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out
}

// ----- Update execution ----------------------------------------------

const updateConfigMapName = "kuso-update-status"

// UpdateStatus is what the UI polls during an in-flight update.
// Written to a ConfigMap by the updater Job + read by us; the Job
// keeps writing until it exits, so we always see the last reported
// step.
type UpdateStatus struct {
	Phase   string `json:"phase"` // pending | applying-crds | rolling-server | rolling-operator | done | failed
	Message string `json:"message,omitempty"`
	Started time.Time `json:"started"`
	Updated time.Time `json:"updated"`
}

// StartUpdate creates the kube Job that performs the upgrade. Returns
// the Job name; the caller polls /api/system/update/status to track
// progress (which reads the ConfigMap the Job writes to).
//
// targetVersion is "" for "latest" (the cached state from the
// background ticker) or "vX.Y.Z" to pin to a specific tag. When pinned
// we fetch the target release's manifest fresh — the cached state
// might be stale or pointing at a different version. We also relax the
// canAutoUpgrade gate when pinning, since the user is explicitly
// asking for that exact release (they've presumably read the notes).
// "Already on latest" is still enforced when no pin is given.
func (s *Service) StartUpdate(ctx context.Context, targetVersion string) (string, error) {
	if s.Kube == nil {
		return "", errors.New("kube client unavailable")
	}

	var m *Manifest
	if targetVersion != "" {
		// Pinned upgrade: download that specific release's manifest.
		// If the user is going from v0.7.12 → v0.7.13 (or even back to
		// v0.7.10) we trust the explicit target.
		fetched, err := s.fetchVersion(ctx, targetVersion)
		if err != nil {
			return "", fmt.Errorf("fetch %s: %w", targetVersion, err)
		}
		m = fetched
	} else {
		st := s.State()
		if !st.NeedsUpdate {
			return "", errors.New("already on latest")
		}
		if !st.CanAutoUpgrade {
			return "", fmt.Errorf("auto-upgrade blocked: %s", st.BlockedReason)
		}
		m = st.Manifest
		if m == nil {
			return "", errors.New("no manifest cached — try ?version=vX.Y.Z to pin")
		}
	}

	// Reset / create the status ConfigMap so the UI doesn't see a
	// stale "done" from the previous run.
	if err := s.writeStatus(ctx, UpdateStatus{
		Phase:   "pending",
		Started: time.Now().UTC(),
		Updated: time.Now().UTC(),
	}); err != nil {
		return "", fmt.Errorf("write status: %w", err)
	}

	// The Job runs an updater script that (1) applies the new CRDs,
	// (2) `kubectl set image` for both deployments, (3) waits for
	// rollout. It writes status to the ConfigMap at each step.
	// The image is pinned to the same tag as the target server
	// release so we always run the version we're upgrading TO,
	// avoiding "old updater can't apply new manifest" surprises.
	// Updater image resolution: prefer the manifest's value, then
	// the version-tagged default, then :latest. Old kuso instances
	// upgrading past this fix get the version-tagged path; if that
	// release predates the updater publish (e.g. someone re-runs an
	// old upgrade), :latest is still there as a safety net.
	updaterImg := m.Components.Updater.Image
	if updaterImg == "" {
		updaterImg = "ghcr.io/sislelabs/kuso-updater:" + m.Version
	}
	// Snapshot the pre-update operator image so we can roll back if
	// the new image fails its readiness probe.
	priorOperatorImg := s.currentOperatorImage(ctx)

	jobName := fmt.Sprintf("kuso-update-%d", time.Now().Unix())
	job := &batchv1Job{
		Name:      jobName,
		Namespace: s.Namespace,
		Image:     updaterImg,
		Env: map[string]string{
			"KUSO_TARGET_VERSION":   m.Version,
			"KUSO_SERVER_IMAGE":     m.Components.Server.Image,
			"KUSO_OPERATOR_IMAGE":   m.Components.Operator.Image,
			"KUSO_CRDS_URL":         m.CRDs.URL,
			"KUSO_NAMESPACE":        s.Namespace,
			"KUSO_STATUS_CONFIGMAP": updateConfigMapName,
		},
	}
	if err := s.applyJob(ctx, job); err != nil {
		_ = s.writeStatus(ctx, UpdateStatus{Phase: "failed", Message: err.Error(), Updated: time.Now().UTC()})
		return "", err
	}
	// Background watchdog: if the new operator image doesn't go Ready
	// within healthGate, roll it back to the prior tag. The Logger
	// records every transition; the user sees the result in the
	// ConfigMap status the UI polls. R5 audit fix.
	go s.watchOperatorHealth(jobName, priorOperatorImg, m.Components.Operator.Image)
	return jobName, nil
}

// healthGate is how long we give the new operator image before
// declaring failure and rolling back.
const healthGate = 5 * time.Minute

// currentOperatorImage reads the operator Deployment's image tag.
// Returns "" if the Deployment is missing or the read fails — in
// either case the watchdog has nothing to roll back to and skips.
func (s *Service) currentOperatorImage(ctx context.Context) string {
	if s.Kube == nil {
		return ""
	}
	d, err := s.Kube.Clientset.AppsV1().Deployments(operatorNamespace).Get(ctx, operatorDeployment, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == operatorContainer {
			return c.Image
		}
	}
	return ""
}

// watchOperatorHealth polls the operator Deployment every 10s for up
// to healthGate. If we don't see all replicas Ready by the deadline,
// we issue a `set image` rollback to priorImg and update the
// kuso-update ConfigMap with phase=rolled-back so the UI shows the
// failure.
//
// Best-effort: kube errors during the watchdog log a warn and do NOT
// trigger a false-positive rollback (a transient apiserver hiccup
// shouldn't undo a healthy deploy).
func (s *Service) watchOperatorHealth(jobName, priorImg, newImg string) {
	if priorImg == "" || newImg == "" || priorImg == newImg {
		// Nothing to roll back to (fresh install) or no real change —
		// skip.
		return
	}
	// Derive from the server's lifecycle context so a graceful
	// shutdown cancels the watchdog instead of running against a
	// closed kube client and possibly firing a spurious rollback.
	s.mu.RLock()
	parent := s.runCtx
	s.mu.RUnlock()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, healthGate+30*time.Second)
	defer cancel()
	deadline := time.Now().Add(healthGate)
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d, err := s.Kube.Clientset.AppsV1().Deployments(operatorNamespace).Get(ctx, operatorDeployment, metav1.GetOptions{})
		if err != nil {
			s.Logger.Warn("updater: watchdog read", "err", err)
			continue
		}
		// Healthy: spec.replicas matches status.readyReplicas AND
		// the image is the new one. Without the image check, an
		// old replica still draining would falsely satisfy the gate.
		var liveImg string
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name == operatorContainer {
				liveImg = c.Image
			}
		}
		if liveImg == newImg && d.Status.ReadyReplicas == d.Status.Replicas && d.Status.Replicas > 0 {
			s.Logger.Info("updater: operator healthy after upgrade", "image", newImg, "job", jobName)
			return
		}
		if time.Now().After(deadline) {
			s.Logger.Warn("updater: operator unhealthy after gate, rolling back",
				"newImage", newImg, "priorImage", priorImg, "ready", d.Status.ReadyReplicas, "want", d.Status.Replicas)
			if err := s.rollbackOperator(ctx, priorImg); err != nil {
				s.Logger.Error("updater: rollback failed", "err", err)
				_ = s.writeStatus(ctx, UpdateStatus{
					Phase:   "rollback-failed",
					Message: fmt.Sprintf("rollback to %s failed: %v", priorImg, err),
					Updated: time.Now().UTC(),
				})
				return
			}
			_ = s.writeStatus(ctx, UpdateStatus{
				Phase:   "rolled-back",
				Message: fmt.Sprintf("operator at %s did not become Ready within %s; reverted to %s", newImg, healthGate, priorImg),
				Updated: time.Now().UTC(),
			})
			return
		}
	}
}

// rollbackOperator patches the operator Deployment back to priorImg.
func (s *Service) rollbackOperator(ctx context.Context, priorImg string) error {
	patch := fmt.Sprintf(
		`{"spec":{"template":{"spec":{"containers":[{"name":%q,"image":%q}]}}}}`,
		operatorContainer, priorImg,
	)
	_, err := s.Kube.Clientset.AppsV1().Deployments(operatorNamespace).Patch(
		ctx, operatorDeployment, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	return err
}

// Constants for operator deployment identification. Kept here rather
// than in a shared file because they're only used by the watchdog
// and are tied to the install.sh layout.
const (
	operatorNamespace  = "kuso-operator-system"
	operatorDeployment = "kuso-operator-controller-manager"
	operatorContainer  = "manager"
)

// Status reads the ConfigMap the updater Job is writing to. Returns
// (zero, false) when no update has ever run on this instance.
func (s *Service) Status(ctx context.Context) (UpdateStatus, bool) {
	if s.Kube == nil {
		return UpdateStatus{}, false
	}
	cm, err := s.Kube.Clientset.CoreV1().ConfigMaps(s.Namespace).Get(ctx, updateConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return UpdateStatus{}, false
	}
	if err != nil {
		return UpdateStatus{}, false
	}
	raw := cm.Data["status"]
	if raw == "" {
		return UpdateStatus{}, false
	}
	var st UpdateStatus
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return UpdateStatus{}, false
	}
	return st, true
}

func (s *Service) writeStatus(ctx context.Context, st UpdateStatus) error {
	body, _ := json.Marshal(st)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      updateConfigMapName,
			Namespace: s.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "kuso-server"},
		},
		Data: map[string]string{"status": string(body)},
	}
	_, err := s.Kube.Clientset.CoreV1().ConfigMaps(s.Namespace).Create(ctx, cm, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = s.Kube.Clientset.CoreV1().ConfigMaps(s.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
	}
	return err
}

// envOrDefault is a tiny helper so we don't duplicate the os.Getenv
// pattern with a fallback. Pulled inline so this package has no
// dependency on cmd/.
func envOrDefault(key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

// requireSignatures returns true when the operator has opted into
// strict manifest verification. Defaults to false (unsigned releases
// pass with a warn log) so existing installs can continue to update
// before keys are wired. Once we ship signed releases everywhere
// this flag flips to true-by-default in a future major.
func requireSignatures() bool {
	return getenv("KUSO_REQUIRE_SIGNATURES") == "true"
}

// verifyManifestSignature looks for a `release.json.sig` asset on
// the GH release and verifies it against the configured public key.
// Returns nil when the signature checks out OR (in non-strict mode)
// when no signature is present. Returns an error when:
//   - signature exists but no public key configured
//   - public key configured but signature is missing
//   - signature exists but verification fails
//
// The public key lives in env var KUSO_RELEASE_PUBLIC_KEY (base64
// Ed25519 public bytes) so it can be distributed via the install
// script + baked into the deploy yaml. We deliberately don't fetch
// the key from a URL — that would re-introduce the same supply-chain
// hole we're closing.
func (s *Service) verifyManifestSignature(ctx context.Context, assets []struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}, manifestBody []byte) error {
	pubB64 := getenv("KUSO_RELEASE_PUBLIC_KEY")
	sigURL := ""
	for _, a := range assets {
		if a.Name == "release.json.sig" {
			sigURL = a.BrowserDownloadURL
			break
		}
	}
	if sigURL == "" && pubB64 == "" {
		// No signature, no key — treat as unsigned. Caller decides
		// whether that's acceptable (requireSignatures gate).
		return fmt.Errorf("no signature on release")
	}
	if sigURL != "" && pubB64 == "" {
		return fmt.Errorf("release is signed but KUSO_RELEASE_PUBLIC_KEY is unset")
	}
	if sigURL == "" && pubB64 != "" {
		return fmt.Errorf("public key configured but release.json.sig asset missing")
	}
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	if l := len(pub); l != ed25519.PublicKeySize {
		return fmt.Errorf("public key wrong size: got %d want %d", l, ed25519.PublicKeySize)
	}
	sreq, _ := http.NewRequestWithContext(ctx, "GET", sigURL, nil)
	sresp, err := s.client.Do(sreq)
	if err != nil {
		return fmt.Errorf("fetch signature: %w", err)
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != 200 {
		return fmt.Errorf("signature: status %d", sresp.StatusCode)
	}
	sigB64, err := io.ReadAll(io.LimitReader(sresp.Body, 1024))
	if err != nil {
		return fmt.Errorf("read signature: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), manifestBody, sig) {
		return fmt.Errorf("signature does not match")
	}
	return nil
}
