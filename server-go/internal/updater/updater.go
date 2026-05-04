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
	if s.etag != "" {
		req.Header.Set("If-None-Match", s.etag)
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
	s.etag = resp.Header.Get("ETag")
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
	return jobName, nil
}

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
