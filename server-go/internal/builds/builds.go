// Package builds owns the KusoBuild CR lifecycle: list/get/create plus a
// status poller that watches the rendered Job (via batch/v1) and
// promotes the image tag onto the production env on success.
//
// Phase 5 ships build creation that accepts an explicit ref or a public-
// repo branch (synthetic ref). Branch → SHA resolution via the GitHub
// App lands in Phase 6 once the github package is wired.
//
// FUTURE: this package + the helm-operator-driven Job rendering for
// KusoBuild are the most likely subsystem to move to a Go controller
// (controller-runtime + manager.Reconciler). The pressure points are:
//
//   - annotations-as-status races (the operator's helm-release status
//     and our own status patches interleave; a Go reconciler would
//     own both writes and avoid the merge dance);
//   - bulk-create paths (Coolify import commits ~50 KusoBuilds in
//     quick succession; the helm-operator's 3min reconcile + chart
//     render-per-CR is the slow path).
//
// Out of scope for v0.9; track in the followup review as A-P1-7.
package builds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/failures"
	"kuso/server/internal/kube"
	"kuso/server/internal/metrics"
	"kuso/server/internal/releaserun"
	"kuso/server/internal/serverstate"
)

// TokenMinter mints a short-lived GitHub installation token used by the
// build's clone init container. The builds package depends on the
// interface (not the github package directly) so the github client
// stays optional — when it's nil, builds still go out, but with an
// empty token (the clone will fail clearly instead of pods wedging on
// "secret not found").
type TokenMinter interface {
	MintInstallationToken(ctx context.Context, installationID int64) (string, error)
}

// InstallationResolver looks up which GitHub App installation has
// access to a given repo. Lets the build path auto-bind a service
// to the right installation when the user didn't pin one explicitly.
//
// Returns (0, nil) when no match — caller falls through to unauth
// clone (works for public repos). Errors are logged but non-fatal.
type InstallationResolver interface {
	ResolveInstallationForRepo(ctx context.Context, owner, repo string) (int64, error)
}

// RepoAccessChecker preflights "can the GitHub App actually read this
// repo?" against the live API. Used to fail-fast before spinning up
// kaniko — without it, an unreachable repo costs the user a 30-60s
// pod-schedule + clone-fail cycle just to learn what one HTTP round-
// trip can answer in ~150ms.
//
// nil is fine — when wired we surface clearer errors; when unwired
// the build path falls through to the kaniko clone (which fails
// noisily but gets the same job done).
type RepoAccessChecker interface {
	CheckRepoAccess(ctx context.Context, installationID int64, owner, repo string) error
}

// RegistryHost is the in-cluster registry every build pushes to. The
// helm chart for kuso-registry exposes this as a Service.
const RegistryHost = "kuso-registry.kuso.svc.cluster.local:5000"

// Build phase + timing live on annotations because helm-operator owns
// .status on every CR and overwrites the whole stanza on each
// reconcile. Keys are namespaced under kuso.sislelabs.com/build-* so
// they don't collide with anything else that ends up on the object.
const (
	annPhase        = "kuso.sislelabs.com/build-phase"
	annCompletedAt  = "kuso.sislelabs.com/build-completed-at"
	annStartedAt    = "kuso.sislelabs.com/build-started-at"
	annMessage      = "kuso.sislelabs.com/build-message"
	annSupersededBy = "kuso.sislelabs.com/superseded-by"
	// annClassification carries the JSON-encoded failures.Classification
	// (kind + tab + summary + actionable Remediation) for a failed build,
	// so the Deployments tab can surface the fix when the user opens the
	// build — not just in the bell-popover notification.
	annClassification = "kuso.sislelabs.com/build-classification"
	// detectedEnv carries a JSON-encoded []string of env-var names
	// the build-time scanner surfaced from the source repo. UI uses
	// this to flag "you reference X but it isn't set" at the
	// EnvVarsEditor level. Stored at the build CR (not the service)
	// so re-builds refresh the list and we keep an audit trail.
	annDetectedEnv   = "kuso.sislelabs.com/detected-env"
	annDetectedEnvAt = "kuso.sislelabs.com/detected-env-at"
	// Trigger context — surfaces in BuildSummary so users can see who
	// kicked off a build and why. Helps when a teammate asks "who
	// broke prod at 3pm" — the deployments tab now answers that
	// without git/issue archaeology.
	annTriggerSource = "kuso.sislelabs.com/build-triggered-by"
	annTriggerUser   = "kuso.sislelabs.com/build-triggered-by-user"
	annCommitMessage = "kuso.sislelabs.com/build-commit-message"
)

// buildPhase returns the kuso-tracked phase annotation.
func buildPhase(b *kube.KusoBuild) string {
	if b == nil {
		return ""
	}
	return b.Annotations[annPhase]
}

// Service handles the build domain. Construct via New.
type Service struct {
	Kube       *kube.Client
	Namespace  string
	NSResolver *kube.ProjectNamespaceResolver
	// Tokens mints fresh github installation tokens for the build's
	// clone init container. Optional — nil means we still create the
	// expected secret (empty value) so pods start, but private repos
	// will fail to clone.
	Tokens TokenMinter

	// InstallResolver maps a github.com repo URL to the App
	// installation that has access to it. Optional — nil falls
	// through to the project/service spec InstallationID (which may
	// also be 0 for public repos).
	InstallResolver InstallationResolver

	// RepoAccess preflights "can this installation actually read the
	// repo?" before we spin up the kaniko Job. Optional — nil skips
	// the preflight and we get the same error after a slower failed
	// clone. The github.Client implements this interface.
	RepoAccess RepoAccessChecker

	// MaxConcurrentBuilds is the fallback cap when no Settings
	// provider is wired (CLI tests, legacy main.go). Production
	// reads the live value from Settings.GetBuildSettings on each
	// admission, so the admin's /settings change takes effect on
	// the next build without a server restart.
	//
	// 0 disables the cap (matches pre-v0.7.17 behaviour). Override
	// per project via the kuso.sislelabs.com/build-max-concurrent
	// annotation on the KusoProject CR.
	MaxConcurrentBuilds int
	// AdmitTimeout is how long Create waits for a build slot to open
	// before returning ErrConflict. Zero defaults to 60s — long
	// enough to absorb a monorepo push storm against a 1-min build,
	// short enough to bound webhook retry latency.
	AdmitTimeout time.Duration

	// Settings reads admin-tunable knobs from the DB. When non-nil,
	// build resource limits + the cluster-wide concurrent cap come
	// from here instead of the static MaxConcurrentBuilds field.
	// Cached for 30s per process so admin saves propagate quickly
	// without burning DB reads on every Create.
	Settings SettingsProvider

	// settingsCache memoizes the last read so per-Create reads
	// don't hit Postgres on every webhook tick.
	settingsMu       sync.Mutex
	settingsCache    *cachedBuildSettings
	settingsCacheTTL time.Duration

	// inFlight dedupes concurrent Create calls keyed on
	// (project, service, sha). When a webhook retries (GitHub does so
	// on 5xx) or two callers race the same build, the second caller
	// waits on the first's outcome instead of creating a duplicate
	// KusoBuild + clone-token Secret. Keys live for the duration of
	// Create only; freed in defer regardless of outcome.
	inFlight sync.Map

	// serviceLocks serializes Create calls for the same (project,
	// service) regardless of SHA. Without this, two simultaneous
	// pushes of different commits both see "no active build" in the
	// active-check, both create CRs without the queued label, both
	// render kaniko Jobs in parallel — defeating the queue. The
	// per-service mutex closes the TOCTOU window between
	// findActiveForService and CreateKusoBuild.
	//
	// We use a map of *sync.Mutex (not sync.Map of mutexes) because
	// we need to lock for the duration of Create — sync.Map's value
	// retrieval is not lock-friendly. The outer mutex protects the
	// map; the per-service mutex protects the active-check + create
	// critical section. Hot-path cost: one map lookup + one Lock()
	// per Create, which is microseconds.
	serviceLocksMu sync.Mutex
	serviceLocks   map[string]*serviceLockEntry

	// (Removed in v0.8.10) admitOnce + admitSem — the in-memory
	// semaphore was replaced with a kube-list-based admission gate
	// in admitBuild. Cluster reality is the only source of truth
	// that survives kuso-server restarts and operator-rendered
	// Job pods that bypass Create.

	// Notifier receives build.superseded events when a new build for
	// the same (project, service) cancels an in-flight predecessor.
	// Optional: nil → silent. The Poller has its own Notifier slot
	// for build.{succeeded,failed} events.
	Notifier EventEmitter

	// RecordLookup resolves an archived build summary when the live
	// KusoBuild CR has been GC'd by retention — lets Rollback target a
	// build whose CR is gone but whose image still exists in the
	// registry (within imageRetentionWindow). Optional: nil → rollback
	// only works against live CRs (pre-v0.17.x behaviour).
	RecordLookup BuildRecordLookup
}

// BuildRecordLookup fetches one archived build's roll-back-relevant
// fields by CR name. Implemented by an adapter over db.DB. Returns
// ok=false when no record exists.
type BuildRecordLookup interface {
	GetBuildImage(ctx context.Context, project, buildName string) (repo, tag, phase string, ok bool, err error)
}

// inFlightEntry is the value side of inFlight: a channel that closes
// when the leader's Create returns, plus its result. Followers block
// on done and read result/err. We don't bother coalescing the response
// (returning the leader's CR pointer to followers) because Create's
// caller treats the body as advisory — what matters is that we don't
// spawn a second build pod for the same SHA.
type inFlightEntry struct {
	done chan struct{}
}

// SettingsProvider is the read-side of the admin-tunable build
// settings (db.DB satisfies it). Held as an interface so the builds
// package doesn't pull in db, which would force every test to spin
// up Postgres just to construct a Service.
type SettingsProvider interface {
	GetBuildSettings(ctx context.Context) (BuildSettingsView, error)
}

// BuildSettingsView mirrors db.BuildSettings without importing db.
// Carried through this package as the hot-path config used by
// admitBuild + the chart-render path that overrides spec.resources.
type BuildSettingsView struct {
	MaxConcurrent int
	MemoryLimit   string
	MemoryRequest string
	CPULimit      string
	CPURequest    string
	// External registry overrides. When RegistryAuthSecret is set,
	// builds push to RegistryHost using credentials from the named
	// Secret instead of the in-cluster anonymous kuso-registry. The
	// Secret must contain `.dockerconfigjson` (kaniko) AND
	// `cnb_registry_auth` (CNB lifecycle JSON env). Empty values
	// keep the in-cluster default.
	RegistryAuthSecret string
	RegistryHost       string
}

type cachedBuildSettings struct {
	view    BuildSettingsView
	expires time.Time
}

// loadSettings returns the live build settings, hitting the DB at
// most once per settingsCacheTTL (default 30s). When no Settings
// provider is wired the static MaxConcurrentBuilds + chart defaults
// are returned so the legacy code path keeps working.
func (s *Service) loadSettings(ctx context.Context) BuildSettingsView {
	if s.Settings == nil {
		return BuildSettingsView{MaxConcurrent: s.MaxConcurrentBuilds}
	}
	ttl := s.settingsCacheTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	// Hold the lock across the DB read so concurrent callers under a
	// build storm coalesce on the cache instead of all racing through
	// to GetBuildSettings — the previous unlock-read-relock pattern
	// defeated the whole purpose of the cache. One Postgres roundtrip
	// per TTL is fine; ten in parallel saturates the pool.
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if s.settingsCache != nil && time.Now().Before(s.settingsCache.expires) {
		return s.settingsCache.view
	}
	v, err := s.Settings.GetBuildSettings(ctx)
	if err != nil {
		// Fall back to static config; never fail a build because
		// we couldn't reach Postgres. The cache stays empty so the
		// next call retries.
		return BuildSettingsView{MaxConcurrent: s.MaxConcurrentBuilds}
	}
	s.settingsCache = &cachedBuildSettings{view: v, expires: time.Now().Add(ttl)}
	return v
}

// InvalidateSettingsCache drops the in-memory build-settings cache.
// Call from the settings handler after a successful write so the next
// build picks up the new memory limit / concurrency cap on the next
// Create instead of waiting up to 30s for the TTL. The settings
// admin who just bumped the limit shouldn't watch the next 10 builds
// OOM with the old value.
func (s *Service) InvalidateSettingsCache() {
	if s == nil {
		return
	}
	s.settingsMu.Lock()
	s.settingsCache = nil
	s.settingsMu.Unlock()
}

// inFlightKey produces the dedup key. SHA is the natural primary key
// for a build (image tag derives from it); project + service prevent
// false-positive collisions when two services in different projects
// happen to share a SHA (rare but possible with monorepos).
func inFlightKey(project, service, sha string) string {
	return project + "/" + service + "/" + sha
}

// serviceLockEntry pairs the per-service mutex with a last-access
// timestamp so the periodic GC can drop entries for services that
// haven't built in a long time. Without GC, the map grows by one
// entry per (project, service) ever seen — including ephemeral
// preview envs — and becomes a slow memory leak on churn-heavy
// clusters.
type serviceLockEntry struct {
	mu         *sync.Mutex
	lastAccess time.Time
}

// serviceLockFor returns the per-service mutex, creating it on first
// access. Stamps lastAccess so the GC can find idle entries. The
// returned mutex is safe to lock outside the map mutex; we only
// guard the map shape here.
func (s *Service) serviceLockFor(project, service string) *sync.Mutex {
	key := project + "/" + service
	s.serviceLocksMu.Lock()
	defer s.serviceLocksMu.Unlock()
	if s.serviceLocks == nil {
		s.serviceLocks = map[string]*serviceLockEntry{}
	}
	e, ok := s.serviceLocks[key]
	if !ok {
		e = &serviceLockEntry{mu: &sync.Mutex{}}
		s.serviceLocks[key] = e
	}
	e.lastAccess = time.Now()
	return e.mu
}

// gcServiceLocks drops lock entries older than `maxAge` whose mutex
// can be acquired in non-blocking mode (i.e. nobody is currently
// using it). Safe to call on a timer; cheap (one TryLock per entry).
func (s *Service) gcServiceLocks(maxAge time.Duration) int {
	now := time.Now()
	s.serviceLocksMu.Lock()
	defer s.serviceLocksMu.Unlock()
	var dropped int
	for key, e := range s.serviceLocks {
		if now.Sub(e.lastAccess) < maxAge {
			continue
		}
		// Only drop if currently unlocked — TryLock returns true
		// when it acquires; we drop the entry while holding it,
		// which is fine because no other path can grab the same
		// pointer (the map mutex is held).
		if !e.mu.TryLock() {
			continue
		}
		e.mu.Unlock()
		delete(s.serviceLocks, key)
		dropped++
	}
	return dropped
}

// RunServiceLockGC starts a goroutine that periodically GCs idle
// service-lock entries. Call once in main.go after constructing the
// builds.Service. Exits cleanly when ctx is canceled.
func (s *Service) RunServiceLockGC(ctx context.Context) {
	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.gcServiceLocks(2 * time.Hour)
			}
		}
	}()
}

// New constructs a builds.Service with a default namespace fallback.
//
// MaxConcurrentBuilds + AdmitTimeout are zero on the bare struct, but
// main.go ALWAYS sets them before the service is used: MaxConcurrentBuilds
// from KUSO_BUILD_MAX_CONCURRENT or the adaptive default (max(2,
// allocatableCPU/4)), and AdmitTimeout from KUSO_BUILD_ADMIT_TIMEOUT_SECONDS
// (default 60s). The live cap is admin-tunable via the Settings table.
// Do not treat the zero-value here as the production default — it is not.
func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
}

// admitBuild gates Create against the cluster-wide concurrent-build
// cap. Returns ErrConflict immediately when at-or-over the cap (i.e.
// don't queue at the admission layer — the queue model already
// handles "wait for capacity" by stamping new CRs as queued). Returns
// nil on admission with a no-op release function (we no longer hold
// a semaphore slot for the duration of CR creation; cluster reality
// is the source of truth).
//
// Why we replaced the in-memory semaphore (v0.8.10):
//
//   - The buffered-channel semaphore lived in one kuso-server replica's
//     memory. After a kuso-server restart, the new pod started with
//     an empty semaphore even though build pods from before the
//     restart were still consuming node resources. On v0.8.9 a
//     redeploy-after-roll triggered 3 simultaneous builds that
//     OOM-thrashed the host because the new replica thought capacity
//     was free.
//   - Operator-rendered Job pods (queued→pending→running by the
//     dispatcher tick) bypassed admitBuild entirely.
//   - With per-service queueing, the only thing left to gate is the
//     promote rate, which the dispatcher already does (one promote
//     per tick per service). The cluster-wide cap is a final
//     safety belt against pathological cross-service storms.
//
// New strategy: count actual running build pods cluster-wide. The
// label app.kubernetes.io/component=kusobuild tags every kaniko Job
// pod the chart renders; one apiserver list per Create is cheap
// (~5ms on a small cluster) and is always correct.
// admitBuild reports whether the cluster is currently at its
// build-concurrency ceiling. Returns capHit=true when full so the
// caller can stamp the new CR as queued; the dispatcher promotes it
// later. Pre-fix this returned ErrConflict on cap-hit, which made
// Redeploy fail with 409 instead of queueing — the user saw "cluster
// at build concurrency cap (1 active, cap 1)" toasts and had to wait
// + retry manually. Now we always admit and let the queue absorb the
// burst.

// Errors mirroring the rest of the codebase.
var (
	ErrNotFound = errors.New("builds: not found")
	ErrInvalid  = errors.New("builds: invalid")
	// ErrConflict is returned when an identical build (same project,
	// service, and resolved SHA) is already being created. The HTTP
	// layer maps this to 409; the GitHub webhook dispatcher swallows
	// it because a retried delivery hitting an in-flight build is a
	// success, not a failure.
	ErrConflict = errors.New("builds: conflict")
)

// CreateBuildRequest is the body of POST /api/projects/:p/services/:s/builds.
// Trigger context fields are populated by the handler (user from
// session, webhook from the github controller) — they're not user-
// supplied through the JSON body.
type CreateBuildRequest struct {
	Branch          string `json:"branch,omitempty"`
	Ref             string `json:"ref,omitempty"`
	TriggeredBy     string `json:"-"` // user|webhook|api|system
	TriggeredByUser string `json:"-"`
	CommitMessage   string `json:"-"`
	// DryRun runs the build through compile + image-layer assembly
	// but skips the registry push and env promotion. The build CR
	// carries spec.dryRun=true; the buildkit container picks up
	// `output=type=image,push=false` from that; the poller treats
	// a successful dry-run as terminal-with-no-promotion. Surfaces
	// "does this PR build?" feedback without burning registry
	// storage or rolling prod.
	DryRun bool `json:"dryRun,omitempty"`
}

// shaRE matches a full 40-char git SHA.
var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// List returns the builds for a project (and optionally for a single
// service inside it), newest first.
func (s *Service) List(ctx context.Context, project, service string) ([]kube.KusoBuild, error) {
	pairs := map[string]string{kube.LabelProject: project}
	if service != "" {
		pairs[kube.LabelService] = project + "-" + service
	}
	out, err := s.Kube.ListKusoBuildsByLabels(ctx, s.nsFor(ctx, project), pairs)
	if err != nil {
		return nil, fmt.Errorf("list builds: %w", err)
	}
	// Newest first by creationTimestamp; ties break on name lexically so
	// the order is stable across calls.
	sort.SliceStable(out, func(i, j int) bool {
		ti := out[i].CreationTimestamp
		tj := out[j].CreationTimestamp
		if ti.Equal(&tj) {
			return out[i].Name > out[j].Name
		}
		return ti.After(tj.Time)
	})
	return out, nil
}

// Create persists a new KusoBuild. Phase 5 limits:
//   - explicit `ref` (40-char SHA) → image tag = first 12 chars
//   - explicit `ref` (anything else) → image tag = ref verbatim
//   - empty ref + branch + no GitHub installation → synthetic
//     "<branch>-<unix-millis>" tag (kaniko clones HEAD of the branch)
//   - branch → SHA via GitHub App is Phase 6
//
// The KusoBuild CR carries the resolved image repository + tag. The
// operator's helm-charts/kusobuild chart renders the kaniko Job; the
// poller picks up its outcome.
func (s *Service) Create(ctx context.Context, project, service string, req CreateBuildRequest) (_ *kube.KusoBuild, err error) {
	start := time.Now()
	defer func() { metrics.ObserveBuildCreate(start, err) }()
	fqn := project + "-" + service
	ns := s.nsFor(ctx, project)
	svcCR, err := s.Kube.GetKusoService(ctx, ns, fqn)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: service %s/%s", ErrNotFound, project, service)
	}
	if err != nil {
		return nil, fmt.Errorf("preflight service: %w", err)
	}
	// KusoProject CR always lives in the home namespace.
	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: project %s", ErrNotFound, project)
	}
	if err != nil {
		return nil, fmt.Errorf("preflight project: %w", err)
	}

	// runtime=image services don't go through kaniko at all — the
	// chart pulls the image straight from the registry. A "redeploy"
	// click for one of these is handled at the env level (image tag
	// re-stamp + helm reconcile) and never lands here. Refusing
	// keeps the build pipeline honest: KusoBuild CRs only exist for
	// services that actually build.
	if svcCR.Spec.Runtime == "image" {
		return nil, fmt.Errorf("%w: runtime=image services don't build — change image.tag and save the service spec to redeploy", ErrInvalid)
	}
	// A runtime=worker service with FromService set reuses a sibling
	// service's image — it has nothing of its own to build. Refusing
	// here (same as runtime=image) keeps KusoBuild CRs only for
	// services that genuinely produce an image.
	if svcCR.Spec.Runtime == "worker" && svcCR.Spec.FromService != "" {
		return nil, fmt.Errorf("%w: worker %q reuses %q's image — trigger a build on %q instead", ErrInvalid, service, svcCR.Spec.FromService, svcCR.Spec.FromService)
	}

	repoURL := ""
	repoPath := "."
	if svcCR.Spec.Repo != nil {
		repoURL = svcCR.Spec.Repo.URL
		if svcCR.Spec.Repo.Path != "" {
			repoPath = svcCR.Spec.Repo.Path
		}
	}
	if repoURL == "" && proj.Spec.DefaultRepo != nil {
		repoURL = proj.Spec.DefaultRepo.URL
	}
	if repoURL == "" {
		return nil, fmt.Errorf("%w: service has no repo URL configured", ErrInvalid)
	}

	branch := req.Branch
	if branch == "" {
		if proj.Spec.DefaultRepo != nil && proj.Spec.DefaultRepo.DefaultBranch != "" {
			branch = proj.Spec.DefaultRepo.DefaultBranch
		} else {
			branch = "main"
		}
	}

	sha := req.Ref
	syntheticRef := !shaRE.MatchString(sha)
	if syntheticRef {
		// Phase 5 cannot resolve branch → SHA via GitHub yet. Synthesize
		// a unique-ish ref. Phase 6 will replace this branch with the
		// real github resolve.
		//
		// Slugify the branch first: it flows straight into spec.Ref, the
		// image tag (ImageTag), and the kuso.sislelabs.com/build-ref Job
		// label. A branch like "deploy/kuso" carries a '/', which is
		// illegal in both a Docker tag and a kube label value — the Job
		// create then fails ("a valid label must consist of alphanumeric
		// characters, '-', '_' or '.'"), no build pod ever appears, and
		// the service is stuck at 0 replicas. shortRef is the same
		// slugifier buildCRName already applies to keep the CR name legal.
		sha = fmt.Sprintf("%s-%s", shortRef(branch), strconv.FormatInt(time.Now().UnixMilli(), 36))
	}

	// Dedup concurrent Create calls for the same (project, service, sha).
	// A retried GitHub webhook (or two webhooks racing the same push) used
	// to spawn duplicate KusoBuild + clone-token secrets — kaniko would
	// fail on the second one with "secret already exists" or worse, both
	// would race to push the same image tag. We only dedup on real SHAs;
	// synthetic refs already carry a unix-ms suffix so they're already
	// unique-by-construction.
	if !syntheticRef {
		key := inFlightKey(project, service, sha)
		entry := &inFlightEntry{done: make(chan struct{})}
		if existing, loaded := s.inFlight.LoadOrStore(key, entry); loaded {
			// Another goroutine is already creating this build. Wait for
			// it and return Conflict so the caller treats this as "no-op,
			// already running" — which is exactly what GitHub's retry
			// path needs.
			prev := existing.(*inFlightEntry)
			select {
			case <-prev.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%w: build for %s/%s@%s already in flight",
				ErrConflict, project, service, shortRef(sha))
		}
		defer func() {
			s.inFlight.Delete(key)
			close(entry.done)
		}()
	}

	// Admission control: cap cluster-wide simultaneous builds so a
	// monorepo push storm doesn't OOM the single-box node. Blocks up
	// to AdmitTimeout for a slot; ErrConflict on timeout. Released
	// when the build CR is created (the kaniko Job pod is what
	// actually consumes node resources, but we hold the slot until
	// CR creation so the operator's helm render is bounded too).
	release, capHit, err := s.admitBuild(ctx, project)
	if err != nil {
		return nil, err
	}
	defer release()

	buildName := buildCRName(project, service, sha)

	// Per-service serialization. Without this, two simultaneous Create
	// calls (different SHAs) both pass findActiveForService with no
	// active build, both decide queued=false, both render Jobs in
	// parallel — defeating the queue model. The mutex covers the
	// active-check through CR creation so the queued/active decision
	// is atomic w.r.t. the etcd state. SHA-level dedup happens above
	// (inFlight); this is the broader serialization needed for
	// different-SHA-same-service.
	svcLock := s.serviceLockFor(project, service)
	svcLock.Lock()
	defer svcLock.Unlock()

	// Coalesce rapid synthetic-ref redeploys. A user spam-clicking
	// Redeploy generates one synthetic ref per click (each carries a
	// unix-ms suffix → unique). Without coalescing, all 10 clicks
	// queue and the user sees a stack of ghost rows. We treat
	// "another build for the same (project, service, branch) created
	// in the last 30s that's still queued/pending/running" as the
	// same-intent retry → return that build, no new CR.
	//
	// Only applies when the caller did NOT specify an explicit ref —
	// webhook-triggered builds carry a real SHA and dedup via the
	// inFlight map above. The 30s window is roughly "two redeploy
	// clicks within human reaction time"; longer windows risk
	// coalescing genuinely-distinct intents.
	if syntheticRef && req.Ref == "" {
		if existing, err := s.findRecentForBranch(ctx, ns, project, fqn, branch, 30*time.Second); err == nil && existing != nil {
			return existing, nil
		}
	}

	// Coolify-aligned queueing: if another build is in flight for this
	// service OR the cluster/project cap is full, mark the new CR as
	// queued instead of refusing it. The build poller's dispatcher
	// (see Poller.dispatchQueued) promotes the queued build once an
	// active slot frees up. The chart doesn't render a Job until the
	// build-state=queued label is removed, so a queued CR consumes
	// no node resources.
	queued := capHit
	if !queued {
		if active, err := s.findActiveForService(ctx, ns, project, fqn); err == nil && active != "" && active != buildName {
			queued = true
		}
	}
	imageRepo := fmt.Sprintf("%s/%s/%s", RegistryHost, project, service)

	// Strategy mirrors KusoService.spec.runtime. The chart switches on
	// `strategy: dockerfile|nixpacks` to pick the kaniko args + the
	// optional nixpacks-plan init container. Empty defaults to dockerfile.
	//
	// `worker` is a *runtime*, not a build strategy — it means "run
	// with a custom command, no Ingress". A worker that builds its own
	// repo (no FromService — guarded above) still produces an image
	// from a Dockerfile; the KusoBuild.spec.strategy CRD enum only
	// accepts dockerfile|nixpacks|buildpacks|static, so mapping the
	// literal "worker" through here produced an invalid CR and a 500.
	// Self-building workers build from a Dockerfile.
	strategy := svcCR.Spec.Runtime
	if strategy == "" || strategy == "worker" {
		strategy = "dockerfile"
	}

	installationID := githubInstallationID(proj, svcCR)
	// Auto-resolve from the GH-app cache when the user didn't pin
	// one. Catches the common "I installed the App on my org but
	// forgot to plumb the installation ID into the project" case —
	// the cache already has the installation→repo map from the
	// install-callback flow, so we can look it up without a network
	// round-trip. Best-effort: a resolver miss falls through to the
	// existing unauth-clone path.
	if installationID == 0 && s.InstallResolver != nil {
		if owner, repoName := splitGithubURL(repoURL); owner != "" {
			if id, err := s.InstallResolver.ResolveInstallationForRepo(ctx, owner, repoName); err == nil && id > 0 {
				installationID = id
			}
		}
	}

	// Preflight: when we have a github URL + an installation, verify
	// the App can actually see the repo. Costs one HTTP round-trip
	// (~150ms); saves the user a 30-60s "kaniko spins, fails to
	// clone, helm uninstalls, status=failed" cycle when the App was
	// never installed on the repo's owner. We never block on a
	// resolver miss for public github URLs (installationID still 0)
	// — those clone unauthenticated and that's fine.
	if installationID > 0 && s.RepoAccess != nil {
		if owner, repoName := splitGithubURL(repoURL); owner != "" {
			if err := s.RepoAccess.CheckRepoAccess(ctx, installationID, owner, repoName); err != nil {
				return nil, fmt.Errorf("%w: github preflight failed for %s/%s: %v — install the kuso GitHub App on this repo's owner OR change the repo URL", ErrInvalid, owner, repoName, err)
			}
		}
	}

	// The chart's clone init container reads $GITHUB_INSTALLATION_TOKEN
	// from a secret named "<release>-token" with key "token". The
	// release name == the KusoBuild CR name. Mint a fresh installation
	// token and write the secret BEFORE creating the CR so the operator's
	// helm render finds it the moment the Job pod schedules.
	if err := s.ensureCloneTokenSecret(ctx, ns, buildName, installationID); err != nil {
		return nil, fmt.Errorf("clone token secret: %w", err)
	}
	// Ensure the per-service build cache PVC exists. Kept best-effort:
	// the kusobuild chart's cache mount is gated on .Values.cache.enabled
	// (set true when the PVC name is non-empty). If the PVC create
	// fails for any reason, we proceed without caching — the build is
	// slower but still correct.
	cachePVC := ""
	if !queued && !buildCacheDisabled(proj) {
		// Only ensure the PVC for the build that will actually run.
		// Queued CRs don't render Jobs, and the PVC is only consumed
		// at Job render time anyway. The dispatcher promote path
		// re-runs ensure when it patches in spec.image (so a queued
		// CR promoted later still gets the cache).
		cachePVC = s.ensureBuildCachePVC(ctx, ns, fqn, svcCR, 5)
	}

	labels := map[string]string{
		"kuso.sislelabs.com/project":   project,
		"kuso.sislelabs.com/service":   fqn,
		"kuso.sislelabs.com/build-ref": shortRef(sha),
	}
	annos := map[string]string{}
	if queued {
		// build-state=queued tells the operator's watch selector to
		// skip reconciling this CR — no Job rendered, no resources
		// consumed. Poller.dispatchQueued promotes it (removes the
		// label, stamps phase=pending) when the active build for the
		// same service finishes.
		labels["kuso.sislelabs.com/build-state"] = "queued"
		annos[annPhase] = "queued"
	}
	// Trigger context — captured from the request handler. user/api/
	// webhook/system distinguishes the four valid sources; the user
	// field is the username for "user" source (otherwise the bot/
	// webhook identity). commitMessage comes from the GitHub webhook
	// when available.
	if req.TriggeredBy != "" {
		annos[annTriggerSource] = req.TriggeredBy
	}
	if req.TriggeredByUser != "" {
		annos[annTriggerUser] = req.TriggeredByUser
	}
	if req.CommitMessage != "" {
		annos[annCommitMessage] = req.CommitMessage
	}
	spec := kube.KusoBuildSpec{
		Project:              project,
		Service:              fqn,
		Ref:                  sha,
		Branch:               branch,
		Repo:                 &kube.KusoRepoRef{URL: repoURL, Path: repoPath},
		GithubInstallationID: installationID,
		Strategy:             strategy,
		Dockerfile:           svcCR.Spec.Dockerfile,
		DryRun:               req.DryRun,
		// Carry strategy-specific configuration from the service
		// spec onto the build CR so the helm chart can render the
		// right command line. Empty pointers leave the chart on
		// its defaults.
		Static:     svcCR.Spec.Static,
		Buildpacks: svcCR.Spec.Buildpacks,
		// Build-time env config (see KusoServiceSpec): --build-arg inputs
		// and the sentinel-baked public-env names. Plain pass-through —
		// the build Job + env chart consume these.
		BuildArgs: svcCR.Spec.BuildArgs,
		PublicEnv: svcCR.Spec.PublicEnv,
	}
	// Build-time env: resolve the service's env vars to literals (reading
	// referenced secrets) and stamp them onto the CR so the build job can
	// bake them into the image. Apps that read env at build (Prisma
	// generate needs DATABASE_URL, Next.js compiles NEXT_PUBLIC_* and
	// validates env) require this. Unresolvable refs are omitted, not fatal.
	// NOTE: these values bake into image layers in the in-cluster registry.
	if be := s.resolveBuildEnv(ctx, ns, svcCR.Spec.EnvVars); len(be) > 0 {
		spec.BuildEnv = be
	}
	if !queued {
		spec.Image = &kube.KusoImage{Repository: imageRepo, Tag: ImageTag(sha)}
	}
	if cachePVC != "" {
		spec.Cache = &kube.KusoBuildCache{PVCName: cachePVC}
	}
	// Admin-tunable build resources. When the operator has saved
	// non-default values via the Settings UI we stamp them onto the
	// CR so the chart's `.Values.resources` picks them up; missing
	// fields fall through to the chart's values.yaml defaults.
	settings := s.loadSettings(ctx)
	// Registry transport. The default in-cluster kuso-registry runs
	// plain HTTP on the cluster Service network, so kaniko needs
	// --insecure to push there. External registries (set via
	// settings.RegistryAuthSecret) MUST be HTTPS — flip allowInsecure
	// off in that case.
	spec.Registry = &kube.KusoBuildRegistry{
		AllowInsecure: settings.RegistryAuthSecret == "",
	}
	if settings.RegistryAuthSecret != "" {
		spec.Auth = &kube.KusoBuildAuth{
			SecretName: settings.RegistryAuthSecret,
			Registry:   settings.RegistryHost,
		}
	}
	if settings.MemoryLimit != "" || settings.MemoryRequest != "" ||
		settings.CPULimit != "" || settings.CPURequest != "" {
		spec.Resources = &kube.KusoBuildResources{}
		if settings.MemoryRequest != "" || settings.CPURequest != "" {
			spec.Resources.Requests = &kube.KusoResourceQty{
				CPU: settings.CPURequest, Memory: settings.MemoryRequest,
			}
		}
		if settings.MemoryLimit != "" || settings.CPULimit != "" {
			spec.Resources.Limits = &kube.KusoResourceQty{
				CPU: settings.CPULimit, Memory: settings.MemoryLimit,
			}
		}
	}
	// Note: when queued, spec.Image is intentionally nil. The kusobuild
	// chart's job.yaml gates Job rendering on `.Values.image.tag` —
	// without the image set, no Job is rendered and the CR sits idle
	// until the dispatcher patches in the image (Poller.dispatchQueued).
	// This is what keeps queued CRs cheap (no kaniko pod, no resources)
	// without needing operator-side changes that require an operator-
	// image rebuild to ship.
	// OwnerReferences: cascade-delete the build CR when its parent
	// KusoService is deleted. Without this, deleting a service leaves
	// orphan KusoBuild records (and any queued builds whose Job never
	// rendered) accumulating in the namespace forever. The
	// kusoservice CRD's helm release stays a peer of the build, so
	// the operator's own uninstall finalizer doesn't conflict.
	owners := []metav1.OwnerReference{}
	if svcCR != nil && svcCR.UID != "" {
		owners = append(owners, metav1.OwnerReference{
			APIVersion: "application.kuso.sislelabs.com/v1alpha1",
			Kind:       "KusoService",
			Name:       svcCR.Name,
			UID:        svcCR.UID,
			// blockOwnerDeletion=false: don't block service delete on
			// build cleanup. controller=false: build CR is not
			// reconciled because of its owner relationship; helm-
			// operator picks it up on its own watch.
		})
	}
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:            buildName,
			Labels:          labels,
			Annotations:     annos,
			OwnerReferences: owners,
		},
		Spec: spec,
	}
	created, cerr := s.Kube.CreateKusoBuild(ctx, ns, build)
	if cerr != nil {
		return created, cerr
	}
	// Adopt the clone-token Secret under the freshly-created KusoBuild CR
	// so it cascade-deletes with the build (retention sweep, project
	// delete, or manual CR delete). The Secret had to be created BEFORE
	// the CR (the operator's helm render needs it the moment the Job
	// pod schedules), so its ownerRef can only be stamped now that the
	// CR's UID exists. Without this the Secret leaks whenever the
	// terminal-transition delete is skipped (build stuck, poller missed
	// the terminal edge) — the Job's TTL does NOT reap it (the Secret is
	// not owned by the Job). Best-effort: a failure here just falls back
	// to the explicit delete on terminal transition.
	if created != nil && created.UID != "" {
		s.adoptCloneTokenSecret(ctx, ns, buildName, string(created.UID))
	}
	return created, nil
}

// adoptCloneTokenSecret stamps an ownerReference to the KusoBuild CR onto
// the <buildName>-token Secret so kube garbage-collects it when the build
// CR is deleted. Best-effort; logged at warn on failure.
func (s *Service) adoptCloneTokenSecret(ctx context.Context, ns, buildName, buildUID string) {
	if s.Kube == nil || s.Kube.Clientset == nil {
		return
	}
	patch := fmt.Sprintf(
		`{"metadata":{"ownerReferences":[{"apiVersion":"application.kuso.sislelabs.com/v1alpha1","kind":"KusoBuild","name":%q,"uid":%q,"blockOwnerDeletion":false,"controller":false}]}}`,
		buildName, buildUID,
	)
	if _, err := s.Kube.Clientset.CoreV1().Secrets(ns).Patch(
		ctx, buildName+"-token", types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	); err != nil && !apierrors.IsNotFound(err) {
		slog.Default().Warn("adopt clone-token secret", "build", buildName, "ns", ns, "err", err)
	}
}

// ImageTag returns the canonical image tag for a ref: 12-char SHA prefix
// for full SHAs, otherwise the ref verbatim. Exported for the GitHub
// webhook handler in Phase 6.
// (ImageTag / buildCRName / shortRef / buildCacheDisabled /
// githubInstallationID / splitGithubURL moved to refs.go alongside
// containerNames / completedCondition. This file keeps the
// stateful Service surface; refs.go holds the pure helpers.)

// ---- Status poller -------------------------------------------------------

// Poller watches kaniko Jobs rendered for KusoBuilds and stamps their
// outcome onto KusoBuild.status. On success it patches the production
// KusoEnvironment with the new image tag.
// EventEmitter is the (notify.Dispatcher.Emit) signature the poller
// calls when a build transitions. Kept as an interface here so the
// builds package doesn't pull in notify (avoids an import cycle if
// notify ever wants build types). Nil emitter = silent.

// tailBuildLogs grabs the last `lines` log lines from the build's
// container pods. Used by the failed-build notification path to
// include a fenced log excerpt in the Discord card.
//
// Best-effort — bounded by a 5s deadline, kube errors are swallowed,
// returns "" when no pod / no logs are available. The archive path
// (archiveLogs) still runs separately for the durable DB snapshot;
// this is a lightweight read of the tail, not a replacement.
func (p *Poller) tailBuildLogs(ctx context.Context, ns string, b *kube.KusoBuild, lines int) string {
	out := p.tailBuildLogLines(ctx, ns, b, lines)
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n")
}

// tailBuildLogLines is the slice-returning variant used by the
// failure classifier. The classifier walks lines in reverse looking
// for known regex patterns and needs them split, not joined. Same
// kube-fetching contract as tailBuildLogs (5s timeout, init containers
// fallback when the main container has no logs).
func (p *Poller) tailBuildLogLines(ctx context.Context, ns string, b *kube.KusoBuild, lines int) []string {
	if p == nil || p.Svc == nil || p.Svc.Kube == nil || b == nil || lines <= 0 {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pods, err := p.Svc.Kube.Clientset.CoreV1().Pods(ns).List(lctx, metav1.ListOptions{
		LabelSelector: kube.LabelSelector(map[string]string{"app.kubernetes.io/instance": b.Name}),
	})
	if err != nil || len(pods.Items) == 0 {
		return nil
	}
	// Sort: most-recently-created pod first. Failed builds with retries
	// can leave older pods behind, and the latest one is where the
	// actual failure lives.
	pod := &pods.Items[0]
	for i := 1; i < len(pods.Items); i++ {
		if pods.Items[i].CreationTimestamp.After(pod.CreationTimestamp.Time) {
			pod = &pods.Items[i]
		}
	}
	tail := int64(lines)
	// We try the main container first (kaniko output), then init
	// containers (clone failures land here). First non-empty wins.
	tryNames := append([]string{}, containerNames(pod.Spec.Containers)...)
	tryNames = append(tryNames, containerNames(pod.Spec.InitContainers)...)
	for _, c := range tryNames {
		req := p.Svc.Kube.Clientset.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container: c,
			TailLines: &tail,
		})
		stream, err := req.Stream(lctx)
		if err != nil {
			continue
		}
		buf := make([]byte, 4096)
		var data []byte
		for {
			n, rerr := stream.Read(buf)
			if n > 0 {
				data = append(data, buf[:n]...)
			}
			if rerr != nil {
				break
			}
		}
		stream.Close()
		s := strings.TrimSpace(string(data))
		if s != "" {
			ls := strings.Split(s, "\n")
			if len(ls) > lines {
				ls = ls[len(ls)-lines:]
			}
			return ls
		}
	}
	return nil
}

// joinLastN joins the last n lines (or all of them if fewer) with
// newlines. Used to turn the classifier's tail slice back into the
// short string the Discord card embeds — keeping the slice as the
// source of truth means the card and the classifier never disagree
// about what "the tail" was.
func joinLastN(lines []string, n int) string {
	if len(lines) == 0 || n <= 0 {
		return ""
	}
	if n > len(lines) {
		n = len(lines)
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// buildFailureURL composes the failed-build deep-link. Same shape as
// buildEventURL but appends a tab hint so the click lands the user
// directly inside the right tab of the service overlay (Variables for
// missing-env, Logs for build_command_failed, Settings for image-pull).
// Empty project/service falls back to "" — the popover row renders a
// non-clickable card in that case.
func buildFailureURL(project, service string, c failures.Classification) string {
	base := buildEventURL(project, service)
	if base == "" {
		return ""
	}
	if c.Tab == "" {
		return base
	}
	url := base + "&tab=" + string(c.Tab) + "&kind=" + string(c.Kind)
	if c.LineNum > 0 {
		url += "&highlight=" + strconv.Itoa(c.LineNum)
	}
	return url
}

// LogArchiver persists, at terminal-phase transition, the artifacts the
// deployments-tab needs to survive KusoBuild CR + pod GC:
//   - SaveBuildLog: the last N lines of the build pod's logs.
//   - SaveBuildRecord: the build SUMMARY (commit/image/status/timing/
//     who) so the tab still lists the build after retention deletes the
//     CR. Takes the already-derived summary fields so builds doesn't
//     import the db package's record type.
//
// Implemented by db.DB; kept as a small interface so tests don't have to
// spin up Postgres.
type LogArchiver interface {
	SaveBuildLog(ctx context.Context, buildName, project, service, phase, logs string) error
	SaveBuildRecord(ctx context.Context, r BuildArchiveRecord) error
}

// BuildArchiveRecord is the summary the poller hands to the archive. A
// builds-package-local mirror of db.BuildRecord so the LogArchiver
// interface doesn't drag the db package into builds.
type BuildArchiveRecord struct {
	BuildName       string
	Project         string
	Service         string
	Branch          string
	CommitSha       string
	CommitMessage   string
	ImageTag        string
	Status          string
	StartedAt       string
	FinishedAt      string
	TriggeredBy     string
	TriggeredByUser string
	ErrorMessage    string
}

type Poller struct {
	Svc      *Service
	Interval time.Duration
	Logger   *slog.Logger
	// Notifier receives build.{started,succeeded,failed} events.
	// Optional: nil → no notifications.
	Notifier EventEmitter
	// LogArchive snapshots the kaniko pod's tail logs into SQLite at
	// terminal phase. Optional: nil → no archive (pod logs vanish on
	// TTL as before).
	LogArchive LogArchiver
	// ReleaseRunner runs the pre-deploy release Job (KusoService.spec.
	// release.command) before promoting the new image tag onto an env's
	// deployment. Optional: nil → release hooks are silently skipped
	// (test fixtures, legacy main.go). Production wires this from
	// releaserun.New(kube.Client).
	ReleaseRunner ReleaseRunner

	// ImageDeleter unties registry image tags for builds that age out of
	// the rollback window (imageRetentionWindow). ImageRecords supplies
	// archived build summaries so the window spans builds whose CR is
	// already gone. Both optional: nil → image-retention sweep is
	// skipped (CR/log retention still runs). Wired in main.go to the
	// in-cluster registry + db adapter.
	ImageDeleter ImageDeleter
	ImageRecords ImageRecordLister

	// fairnessCursor remembers, per namespace, the last project we
	// promoted from. Next tick, dispatchQueued starts the round at
	// the *next* project in alphabetical order. Without this, Go's
	// random map iteration produces local fairness over many ticks
	// but lets a single project win several ticks in a row when the
	// global cap is tight. A stable cursor turns "fair on average"
	// into "fair every tick".
	cursorMu       sync.Mutex
	fairnessCursor map[string]string // ns → last-promoted project

	// archiveQueue dispatches archiveLogs work to a bounded worker
	// pool so a tick that finds 15 builds finishing simultaneously
	// doesn't block the leader poller for tens of seconds while it
	// streams pod logs serially. The buffer is sized for one tick
	// of typical concurrency; on overflow we drop the snapshot, log
	// at warn, and increment a metric — the build's terminal phase
	// is already persisted, only the log archive (a UX nice) is at
	// risk.
	archiveOnce  sync.Once
	archiveQueue chan archiveTask

	// capTickCounter is a small counter the tick uses to throttle the
	// per-service retention sweep. The leader-gated daily SweepFinished
	// covers the age-based path; this counter drives a non-leader cap
	// so KusoBuild CRs can't pile up unbounded if the leader is down
	// for a day. Increments every tick; fires CapBuildsPerService once
	// it crosses capTickInterval.
	capTickMu      sync.Mutex
	capTickCounter int
}

// capTickInterval is how many poller ticks pass between per-service
// retention sweeps. With the default 5s tick this works out to once
// every ~6min — short enough that a runaway redeploy loop can't
// outgrow retention in a typical leader-down window, long enough that
// the kube LIST cost is negligible.
const capTickInterval = 72

// capBuildsPerServiceMax is the per-service KusoBuild retention cap.
// 50 covers the common "last week's worth of builds visible in the
// History panel" use case plus comfortable headroom. Overridable via
// KUSO_BUILD_RETENTION_PER_SERVICE for clusters that want to see more.
const capBuildsPerServiceMax = 50

// imageRetentionWindow is how many of the most-recent SUCCEEDED builds
// per service keep their registry image — the "rollback depth". Older
// builds remain in the Deployments list (DB record) but their image tag
// is pruned, so they can't be rolled back to and don't consume registry
// space. Distinct from capBuildsPerServiceMax (which bounds CR count /
// reconcile cost): this bounds registry storage + rollback depth.
// Overridable via KUSO_BUILD_IMAGE_RETENTION.
const imageRetentionWindow = 5

// archiveTask is one queued snapshot job. The full ctx is captured
// rather than the request ctx because the worker outlives the
// markSucceeded/markFailed callsite.
type archiveTask struct {
	ns    string
	build *kube.KusoBuild
	phase string
}

// archiveWorkers is the size of the bounded pool that drains
// archiveQueue. Four workers cover the steady-state shape (up to
// four builds finishing in the same tick) without piling up enough
// concurrent kube round-trips to look like a stampede on a small
// apiserver.
const archiveWorkers = 4

// archiveBuffer caps the queue depth. Beyond this we drop newer
// snapshots to keep memory bounded. The same upper bound also acts
// as the per-tick coalescing window.
const archiveBuffer = 64

// startArchiveWorkers lazily spins up the bounded pool the first
// time the poller dispatches an archive task. Workers shut down
// when the poller's parent context fires.
func (p *Poller) startArchiveWorkers(ctx context.Context) {
	p.archiveOnce.Do(func() {
		p.archiveQueue = make(chan archiveTask, archiveBuffer)
		for i := 0; i < archiveWorkers; i++ {
			go p.archiveWorker(ctx)
		}
	})
}

func (p *Poller) archiveWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-p.archiveQueue:
			if !ok {
				return
			}
			// Detached timeout per task — workers shouldn't share a
			// single ctx deadline across many in-flight snapshots.
			tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			p.archiveLogs(tctx, t.ns, t.build, t.phase)
			cancel()
		}
	}
}

// queueArchive submits an archive task to the pool. Non-blocking
// when the queue has headroom; on overflow we run synchronously as
// a last-resort fallback so a backed-up pool doesn't lose snapshots
// silently. (synchronous fallback shares the original poller goroutine
// — the same shape this whole change avoids — so we also log at
// warn so operators notice the saturation.)
func (p *Poller) queueArchive(ctx context.Context, ns string, b *kube.KusoBuild, phase string) {
	if p.LogArchive == nil {
		return
	}
	// Archive the build SUMMARY synchronously here (cheap, no I/O beyond
	// one upsert) so deployment history survives the CR's eventual
	// retention delete. Done at the terminal edge regardless of whether
	// the log-stream snapshot below succeeds — a build whose pods are
	// already GC'd still gets its record. Best-effort; a failure just
	// means the tab won't backfill this build after the CR is gone.
	p.archiveRecord(ctx, b, phase)
	p.startArchiveWorkers(ctx)
	t := archiveTask{ns: ns, build: b, phase: phase}
	select {
	case p.archiveQueue <- t:
	default:
		slog.Default().Warn("builds: archive queue saturated; running inline",
			"build", b.Name, "phase", phase, "depth", len(p.archiveQueue))
		p.archiveLogs(ctx, ns, b, phase)
	}
}

// archiveRecord snapshots the build's summary into the durable
// BuildRecord store at terminal phase, so the deployments tab can list
// the build after retention deletes its KusoBuild CR. Field extraction
// mirrors the handler's toBuildSummary (spec + annotation reads) so a
// record-derived row is indistinguishable from a live-CR-derived one.
func (p *Poller) archiveRecord(ctx context.Context, b *kube.KusoBuild, phase string) {
	ann := b.Annotations
	get := func(k string) string {
		if ann == nil {
			return ""
		}
		return ann[k]
	}
	rec := BuildArchiveRecord{
		BuildName:       b.Name,
		Project:         b.Spec.Project,
		Service:         strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-"),
		Branch:          b.Spec.Branch,
		CommitSha:       b.Spec.Ref,
		CommitMessage:   get(annCommitMessage),
		Status:          phase,
		StartedAt:       get(annStartedAt),
		FinishedAt:      get(annCompletedAt),
		TriggeredBy:     get(annTriggerSource),
		TriggeredByUser: get(annTriggerUser),
		ErrorMessage:    get(annMessage),
	}
	if b.Spec.Image != nil {
		rec.ImageTag = b.Spec.Image.Tag
	}
	// Terminal-phase records must never archive with an empty
	// FinishedAt: the annotation lives on the API-server copy and the
	// in-memory `b` can predate the stamp (the transition paths mirror
	// it now, but any future caller can regress). Archive time IS the
	// terminal edge, so "now" is accurate to within a poll tick.
	if rec.FinishedAt == "" {
		switch phase {
		case "succeeded", "failed", "cancelled", "release-failed":
			rec.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		}
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.LogArchive.SaveBuildRecord(rctx, rec); err != nil {
		slog.Default().Warn("builds: archive record", "err", err, "build", b.Name)
	}
}

// Run blocks until ctx is cancelled, ticking every Interval and updating
// any KusoBuild whose phase is not yet succeeded/failed. Returns ctx.Err
// on shutdown. Errors from individual ticks are logged at warn so we
// never silently lose state changes — the previous "_ = err" silenced a
// real bug for an entire test cycle.
func (p *Poller) Run(ctx context.Context) error {
	if p.Interval <= 0 {
		// 5s default: the poller filters by !build-state=done, so each
		// tick only touches in-flight builds (single-digit count even
		// on busy clusters) — cost is negligible. The previous 30s
		// default meant pending → running transitions surfaced ~30s
		// after the kaniko Job actually started, leaving the
		// deployments tab showing PENDING while the WS already
		// streamed "Building stage..." kaniko output. With 5s ticks
		// the chip flips within one UI poll of reality.
		p.Interval = 5 * time.Second
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	tick := time.NewTicker(p.Interval)
	defer tick.Stop()
	for {
		if err := p.tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.Logger.Warn("build poller tick", "err", err)
		}
		// Heartbeat after every tick (even a tick that logged an error —
		// the loop is alive, which is what readyz cares about). A poller
		// goroutine that dies/panics stops stamping; readyz on the
		// leader then goes unready and the pod is recycled, releasing
		// leadership to a healthy replica.
		serverstate.PollerHeartbeat()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (p *Poller) tick(ctx context.Context) error {
	// Walk every project's execution namespace, deduped + including the
	// home namespace so single-tenant clusters keep working.
	//
	// We exclude builds the operator already considers terminal
	// (build-state=done). The label is stamped by markSucceeded /
	// markFailed below and is the same selector the helm-operator
	// watch (operator/watches.yaml) uses to skip reconciliation. With
	// 1000 historical builds in a busy cluster, this turns a full-table
	// list into an indexed scan over the few in-flight rows — and
	// matches what the operator side already does.
	for _, ns := range p.Svc.ScanNamespaces(ctx) {
		p.observeNamespace(ctx, ns)
	}
	// Per-service retention sweep. Throttled to once every
	// `capTickInterval` poller ticks (~6min at the default 5s cadence)
	// so a busy cluster's KusoBuild CR count can't grow unbounded
	// while the leader-gated daily sweep is paused (leader pod gone,
	// lease re-electing, etc.). Runs on every replica regardless of
	// leadership — it's idempotent (delete-by-name) so duplicate work
	// is harmless.
	p.capTickMu.Lock()
	p.capTickCounter++
	doCap := p.capTickCounter >= capTickInterval
	if doCap {
		p.capTickCounter = 0
	}
	p.capTickMu.Unlock()
	if doCap {
		max := capBuildsPerServiceMax
		if v := os.Getenv("KUSO_BUILD_RETENTION_PER_SERVICE"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				max = n
			}
		}
		imgKeep := imageRetentionWindow
		if v := os.Getenv("KUSO_BUILD_IMAGE_RETENTION"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				imgKeep = n
			}
		}
		for _, ns := range p.Svc.ScanNamespaces(ctx) {
			if n, err := CapBuildsPerService(ctx, p.Svc.Kube, ns, max, LogAdapter(p.Logger)); err != nil {
				p.Logger.Warn("build retention sweep", "ns", ns, "err", err)
			} else if n > 0 {
				p.Logger.Info("build retention swept", "ns", ns, "deleted", n, "cap", max)
			}
			// Image-retention sweep: untag images past the rollback
			// window so the registry doesn't grow unbounded. Skipped when
			// the deleter isn't wired (e.g. external-registry clusters or
			// stripped test setups).
			if p.ImageDeleter != nil {
				if n, err := SweepImagesPastWindow(ctx, p.Svc.Kube, ns, p.ImageRecords, p.ImageDeleter, imgKeep, LogAdapter(p.Logger)); err != nil {
					p.Logger.Warn("image retention sweep", "ns", ns, "err", err)
				} else if n > 0 {
					p.Logger.Info("image retention swept", "ns", ns, "untagged", n, "keep", imgKeep)
				}
			}
		}
	}
	return nil
}

// observeNamespace is one poller observe pass over a single namespace:
// list the in-flight builds, advance each through checkBuild, then run
// the queue dispatcher. Extracted from tick so its latency can be
// timed as a unit (kuso_reconcile_observe_duration_seconds) — a
// regression here (a slow apiserver, a checkBuild that started blocking
// on log tailing) shows up as a p99 climb on this histogram.
func (p *Poller) observeNamespace(ctx context.Context, ns string) {
	start := time.Now()
	// All builds in the namespace via the informer cache; we filter
	// out done ones in code (the cache helper takes equality maps
	// only, can't express `!build-state`). The active-build set is
	// small relative to historical builds, so the scan cost is fine.
	raw, err := p.Svc.Kube.ListKusoBuildsByLabels(ctx, ns, nil)
	if err != nil {
		p.Logger.Warn("build poller list", "ns", ns, "err", err)
		metrics.ObserveReconcileObserve(start, err)
		return
	}
	for i := range raw {
		b := &raw[i]
		if b.Labels["kuso.sislelabs.com/build-state"] == "done" {
			continue
		}
		// Defensive: the selector excludes done builds, but a
		// build mid-mark could land here on a race. Keep the
		// in-memory phase check so we skip it cleanly.
		phase := buildPhase(b)
		if phase == "succeeded" || phase == "failed" {
			continue
		}
		if err := p.checkBuild(ctx, ns, b); err != nil && !apierrors.IsNotFound(err) {
			p.logger().Warn("build poller checkBuild", "build", b.Name, "ns", ns, "err", err)
		}
	}
	// Queue dispatcher: promote the oldest queued build per service
	// when no active (running/pending) build exists for it. Runs
	// after the activeBuilds sweep so a build that finished THIS
	// tick has its queued sibling promoted on the next one — keeps
	// the state machine simple at the cost of one tick of latency.
	p.dispatchQueued(ctx, ns)
	metrics.ObserveReconcileObserve(start, nil)
}

// dispatchQueued promotes queued builds to running, in round-robin
// order across projects, when their service has no active build.
//
// Why round-robin (not the simpler per-service fan-out we had before):
//
//   - The cluster-wide cap is enforced at admission, but the queue
//     dispatcher decides which queued CR gets promoted first. Without
//     project-level fairness, a single project pushing 20 services in
//     a monorepo storm wins every tick over a project pushing 1
//     service. With round-robin, project A's first promote, then
//     project B's first, then A's second, etc.
//   - The cursor is namespace-scoped: each project's execution
//     namespace gets its own ordering. This matters because
//     ScanNamespaces calls dispatchQueued once per namespace; the
//     namespace IS the project for multi-namespace deployments.
//   - Within a project we still pick the oldest queued build first
//     (FIFO per service), so the user-visible "first click runs
//     first" invariant is preserved.
//
// Concurrency safety: at most one queued build is promoted per
// (project, service) per tick. The operator's renderer is what
// actually allocates the kaniko Job pod, and the cluster-wide
// admitBuild cap is a final safety belt; this method's job is just
// to keep the promote ordering fair.
//
// Best-effort: kube errors are warn-logged and the next tick retries.
func (p *Poller) dispatchQueued(ctx context.Context, ns string) {
	raw, err := p.Svc.Kube.ListKusoBuildsByLabels(ctx, ns, map[string]string{
		"kuso.sislelabs.com/build-state": "queued",
	})
	if err != nil {
		p.Logger.Warn("build poller queue list", "ns", ns, "err", err)
		return
	}
	if len(raw) == 0 {
		return
	}
	// Group queued builds by (project, service). Sorting keys makes
	// the iteration deterministic so the cursor advances predictably.
	type svcQueue struct {
		project string
		fqn     string
		list    []*kube.KusoBuild
	}
	byKey := map[string]*svcQueue{}
	for i := range raw {
		b := &raw[i]
		key := b.Spec.Project + "/" + b.Spec.Service
		q, ok := byKey[key]
		if !ok {
			q = &svcQueue{project: b.Spec.Project, fqn: b.Spec.Service}
			byKey[key] = q
		}
		q.list = append(q.list, b)
	}
	// Group services by project so we can round-robin at the project
	// level (not the service level).
	byProject := map[string][]*svcQueue{}
	for _, q := range byKey {
		byProject[q.project] = append(byProject[q.project], q)
	}
	if len(byProject) == 0 {
		return
	}
	// Stable project order so the cursor advances predictably.
	projects := make([]string, 0, len(byProject))
	for proj := range byProject {
		projects = append(projects, proj)
	}
	sort.Strings(projects)

	// Rotate the project list so the project AFTER the last one we
	// promoted comes first this tick. This is what makes the schedule
	// fair across ticks (not just within a tick).
	startIdx := p.nextCursorIndex(ns, projects)
	rotated := make([]string, 0, len(projects))
	rotated = append(rotated, projects[startIdx:]...)
	rotated = append(rotated, projects[:startIdx]...)

	// Cap promotions per tick at the cluster build cap. Stops a
	// dispatcher tick from promoting 100 queued builds simultaneously
	// on a fresh, empty cluster — the cluster-wide admitBuild cap
	// would refuse most of them anyway, but limiting here saves the
	// apiserver patch-storm and keeps the promote rate predictable.
	cfg := p.Svc.loadSettings(ctx)
	maxPerTick := cfg.MaxConcurrent
	if maxPerTick <= 0 {
		maxPerTick = 8 // uncapped clusters: still a sane upper bound
	}
	promoted := 0
	lastPromotedProject := ""

	// Re-enforce the SAME concurrency caps admission enforces, here in
	// the dispatch path. Admission only gates the interactive Create
	// flow; a queued CR gets promoted to running by THIS dispatcher, so
	// without re-checking, the queue could promote past the cluster-wide
	// cap (across namespaces) and past a project's per-project cap —
	// exactly the caps admitBuild refuses new builds on. maxPerTick
	// alone doesn't cover this: it bounds promotions per tick but ignores
	// builds already running from prior ticks / other namespaces.
	//
	// clusterCap is the cluster-wide concurrent-build cap (0 = uncapped).
	// runningCluster is the current count of running/pending build pods
	// across every namespace; each successful promote below increments
	// it so a single tick can't blow past the cap.
	clusterCap := cfg.MaxConcurrent
	runningCluster := 0
	if clusterCap > 0 {
		runningCluster = p.Svc.countRunningBuildPodsCluster(ctx)
	}

	// One project per outer iteration; one (oldest queued, no active)
	// build per service within. The two-level loop achieves "round-robin
	// across projects, FIFO within a project" in a single pass.
	for _, proj := range rotated {
		if promoted >= maxPerTick {
			break
		}
		if clusterCap > 0 && runningCluster >= clusterCap {
			break // at the cluster cap — nothing else can promote this tick
		}
		queues := byProject[proj]
		// Per-project cap check (0 = no override). Count active build
		// pods for this project once per project pass; skip the whole
		// project when it's already at its cap. A project gets at most
		// one promote per pass, so a single count is sufficient here.
		projCap := p.Svc.projectBuildCap(ctx, proj)
		if projCap > 0 && p.Svc.countActiveBuildsForProject(ctx, proj) >= projCap {
			continue
		}
		// Stable service order within a project too.
		sort.SliceStable(queues, func(i, j int) bool { return queues[i].fqn < queues[j].fqn })
	innerLoop:
		for _, q := range queues {
			if promoted >= maxPerTick {
				break innerLoop
			}
			if clusterCap > 0 && runningCluster >= clusterCap {
				break innerLoop
			}
			// Active check per service. If anything's running, skip
			// this service this tick.
			active, err := p.Svc.findActiveForService(ctx, ns, q.project, q.fqn)
			if err != nil {
				p.Logger.Warn("build poller queue active check",
					"ns", ns, "service", q.fqn, "err", err)
				continue
			}
			if active != "" {
				continue
			}
			// Oldest first.
			sort.SliceStable(q.list, func(i, j int) bool {
				ti := q.list[i].CreationTimestamp
				tj := q.list[j].CreationTimestamp
				if ti.Equal(&tj) {
					return q.list[i].Name < q.list[j].Name
				}
				return ti.Before(&tj)
			})
			next := q.list[0]
			project := q.project
			fqn := q.fqn
			if p.promoteOne(ctx, ns, project, fqn, next) {
				promoted++
				runningCluster++ // count toward the cluster cap for the rest of this tick
				lastPromotedProject = proj
				// Move on to the NEXT project — fair round-robin
				// means each project gets ONE promote slot per pass.
				break innerLoop
			}
		}
	}
	if lastPromotedProject != "" {
		p.setCursor(ns, lastPromotedProject)
	}
	if promoted > 0 {
		queueDepth := 0
		for _, q := range byKey {
			queueDepth += len(q.list)
		}
		p.Logger.Debug("dispatchQueued",
			"ns", ns, "promoted", promoted, "remainingQueued", queueDepth-promoted)
	}
}

// nextCursorIndex returns the index in `projects` where this tick's
// scheduling should start. If we've never promoted in this namespace,
// start at 0. Otherwise start one past the last project we promoted
// (wrapping). When the last-promoted project is no longer in the
// list (queue drained), fall back to 0.
func (p *Poller) nextCursorIndex(ns string, projects []string) int {
	p.cursorMu.Lock()
	defer p.cursorMu.Unlock()
	if p.fairnessCursor == nil {
		return 0
	}
	last, ok := p.fairnessCursor[ns]
	if !ok {
		return 0
	}
	for i, proj := range projects {
		if proj == last {
			return (i + 1) % len(projects)
		}
	}
	return 0
}

// setCursor records the project we just promoted from so the next
// tick advances past it. The cursor map grows by at most one entry
// per execution namespace; bounded by the project count.
func (p *Poller) setCursor(ns, project string) {
	p.cursorMu.Lock()
	defer p.cursorMu.Unlock()
	if p.fairnessCursor == nil {
		p.fairnessCursor = map[string]string{}
	}
	p.fairnessCursor[ns] = project
}

// promoteOne is the meat of what was inline in the old dispatchQueued
// loop. Returns true on a successful patch (caller advances the
// cursor); false on any kube error (caller logs and tries the next
// service).
func (p *Poller) promoteOne(ctx context.Context, ns, project, fqn string, next *kube.KusoBuild) bool {
	// Promote: remove the queued label (operator's selector now
	// matches it), stamp phase=pending, AND patch in spec.image
	// (the chart's render gate). The image is computed the same
	// way Create computes it for an immediate build — repo +
	// service short name + the Ref's image tag.
	shortName := strings.TrimPrefix(fqn, project+"-")
	imageRepo := fmt.Sprintf("%s/%s/%s", RegistryHost, project, shortName)
	// Ensure the build cache PVC exists at promote time too — if
	// the queued CR was created during a window where the PVC
	// briefly didn't exist (or the Create-time ensure failed),
	// catch it here so the promoted build still gets the cache.
	// Best-effort; empty pvcName means the chart skips the cache
	// mount and runs cold.
	var svcCR *kube.KusoService
	if s, err := p.Svc.Kube.GetKusoService(ctx, ns, fqn); err == nil {
		svcCR = s
	}
	var projCR *kube.KusoProject
	if pp, err := p.Svc.Kube.GetKusoProject(ctx, p.Svc.Namespace, project); err == nil {
		projCR = pp
	}
	cachePVC := ""
	if !buildCacheDisabled(projCR) {
		cachePVC = p.Svc.ensureBuildCachePVC(ctx, ns, fqn, svcCR, 5)
	}
	cachePatch := ""
	if cachePVC != "" {
		cachePatch = fmt.Sprintf(`,"cache":{"pvcName":%q}`, cachePVC)
	}
	patch := fmt.Sprintf(
		`{"metadata":{"labels":{"kuso.sislelabs.com/build-state":null},"annotations":{%q:"pending"}},"spec":{"image":{"repository":%q,"tag":%q}%s}}`,
		annPhase, imageRepo, ImageTag(next.Spec.Ref), cachePatch,
	)
	if _, perr := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, next.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
		p.Logger.Warn("build poller promote queued", "build", next.Name, "ns", ns, "err", perr)
		return false
	}
	p.Logger.Info("build poller promoted queued build", "build", next.Name, "service", fqn)
	return true
}

// buildStuckTimeout bounds how long a non-terminal build may sit with no
// observable Job before the poller force-fails it. A build whose Job never
// rendered (operator down, chart-render reject) or was TTL-reaped before the
// poller saw its terminal condition would otherwise stay pending/running
// forever — and because findActiveForService treats it as active, it blocks
// every subsequent build of the same service behind a zombie. Override via
// KUSO_BUILD_STUCK_TIMEOUT (Go duration, e.g. "45m").
func buildStuckTimeout() time.Duration {
	if v := os.Getenv("KUSO_BUILD_STUCK_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 30 * time.Minute
}

// checkBuild reads the kaniko Job for one build and reconciles status.
// ns is the namespace the KusoBuild + Job live in (determined by the
// project's spec.namespace, looked up by the caller).
func (p *Poller) checkBuild(ctx context.Context, ns string, b *kube.KusoBuild) error {
	job, err := p.Svc.Kube.Clientset.BatchV1().Jobs(ns).Get(ctx, b.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// No Job for this build. Either it hasn't rendered yet (young
			// CR — keep waiting for the operator) or it never will / was
			// TTL-reaped after completing during a poller outage (old CR —
			// force-fail so it stops blocking the per-service queue and
			// becomes eligible for retention sweeps). Without this a build
			// with no Job is polled forever and never reaches a terminal
			// state. See findActiveForService — a non-terminal build counts
			// as active and serializes the whole service's build queue.
			if age := buildAge(b); age > buildStuckTimeout() {
				p.logger().Warn("build force-failed: no Job and CR past stuck-timeout",
					"build", b.Name, "ns", ns, "age", age.String())
				return p.markFailed(ctx, ns, b,
					"build job never appeared (operator unavailable or render rejected) and the build exceeded the stuck-timeout")
			}
			// Young CR, Job not yet rendered — not an error, just wait.
			return nil
		}
		return err
	}
	if cond := completedCondition(job); cond != nil {
		if cond.Type == batchv1.JobComplete {
			return p.markSucceeded(ctx, ns, b)
		}
		return p.markFailed(ctx, ns, b, cond.Message)
	}
	// A failed pod (backoffLimit reached) is terminal even in the brief
	// window before the Job controller stamps the Failed condition. Catch
	// it here so a Job that's TTL-reaped in that window doesn't lose the
	// failure (it would otherwise fall through to the no-op return below
	// and then hit the stuck-timeout path above on a later tick).
	if job.Status.Failed > 0 && job.Status.Active == 0 {
		return p.markFailed(ctx, ns, b, "build pod failed")
	}
	if job.Status.Active > 0 {
		return p.markRunning(ctx, ns, b)
	}
	return nil
}

// buildAge returns how long ago the build CR was created. A zero
// creationTimestamp (unusual — only in tests with hand-built objects)
// yields 0 so the stuck-timeout never trips spuriously.
func buildAge(b *kube.KusoBuild) time.Duration {
	if b == nil || b.CreationTimestamp.IsZero() {
		return 0
	}
	return time.Since(b.CreationTimestamp.Time)
}

// Build phase + timing live on annotations, not .status. helm-operator
// owns .status on every CR it manages (it overwrites the whole stanza
// with conditions + deployedRelease on each reconcile, every 1m for
// kusobuilds), so any .status.phase we write gets obliterated within
// a minute. Annotations are part of metadata, ignored by helm-operator,
// and we read them back the same way we used to read .status.phase.
//
// We also stamp a label `kuso.sislelabs.com/build-state=done` so the
// helm-operator's watch selector (operator/watches.yaml) excludes the
// CR from further reconciles. Without it, completed KusoBuilds get
// reconciled every 60s forever — burning ~10% of one core in a busy
// cluster on dead work.
// archiveLogs snapshots the last 200 lines of the kaniko pod's logs
// into the BuildLog table. Best-effort: kube errors are logged at
// warn and swallowed — the build's terminal phase is the load-bearing
// patch, log archiving is a UX nice-to-have. Selects the pod by the
// helm release-instance label the chart sets to the build CR name;
// concatenates all pods (init container + main container split).
func (p *Poller) archiveLogs(ctx context.Context, ns string, b *kube.KusoBuild, phase string) {
	if p.LogArchive == nil || p.Svc == nil || p.Svc.Kube == nil {
		return
	}
	const tailLines = 200
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pods, err := p.Svc.Kube.Clientset.CoreV1().Pods(ns).List(lctx, metav1.ListOptions{
		LabelSelector: kube.LabelSelector(map[string]string{"app.kubernetes.io/instance": b.Name}),
	})
	if err != nil {
		slog.Default().Warn("builds: archive list pods", "err", err, "build", b.Name)
		return
	}
	// Pull the kubelet's terminated reason from container status BEFORE
	// streaming logs — it's the authoritative answer when a build dies
	// in a way that doesn't reach stdout (OOMKilled, evicted, signal
	// before flush). The log-pattern scan still runs after, but
	// terminatedReason wins for the user-facing message because it's
	// the "real" cause.
	terminatedReason := extractTerminatedReason(pods.Items)
	var combined strings.Builder
	tail := int64(tailLines)
	for i := range pods.Items {
		pod := &pods.Items[i]
		// Each pod has a clone init container + a main kaniko container.
		// We pull from each container individually so failures in init
		// (git clone bad ref, missing token) survive too — the operator
		// would otherwise lose them.
		for _, c := range append(append([]string{}, containerNames(pod.Spec.InitContainers)...), containerNames(pod.Spec.Containers)...) {
			req := p.Svc.Kube.Clientset.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: c,
				TailLines: &tail,
			})
			stream, err := req.Stream(lctx)
			if err != nil {
				continue
			}
			data := make([]byte, 0, 8192)
			buf := make([]byte, 4096)
			for {
				n, rerr := stream.Read(buf)
				if n > 0 {
					data = append(data, buf[:n]...)
				}
				if rerr != nil {
					break
				}
			}
			stream.Close()
			if len(data) == 0 {
				continue
			}
			if combined.Len() > 0 {
				combined.WriteString("\n")
			}
			combined.WriteString("--- container: ")
			combined.WriteString(c)
			combined.WriteString(" ---\n")
			combined.Write(data)
		}
	}
	logs := combined.String()
	// Extract detected-env BEFORE the tail truncation: the env-detect
	// init container emits its sentinel block early in the build, so
	// it'd get cut off when a long kaniko stage drowns the tail.
	if detected := parseDetectedEnv(logs); detected != nil {
		// Stash on the build CR's status. Best-effort; a failure here
		// just means the UI won't see the suggestion list — the build
		// itself is unaffected.
		if err := p.persistDetectedEnv(lctx, ns, b, detected); err != nil {
			slog.Default().Warn("builds: persist detectedEnv", "err", err, "build", b.Name)
		}
	}
	// Last-N-line cap. The kaniko build can spit 5k+ lines in a noisy
	// build (apt-get progress, nix derivations); we want the tail
	// because that's where the failure usually is.
	if lines := strings.Split(logs, "\n"); len(lines) > tailLines {
		logs = strings.Join(lines[len(lines)-tailLines:], "\n")
	}
	if err := p.LogArchive.SaveBuildLog(lctx, b.Name, b.Spec.Project,
		strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-"), phase, logs); err != nil {
		slog.Default().Warn("builds: archive save", "err", err, "build", b.Name)
	}
	// On failed builds, replace the generic "Job has reached the
	// specified backoff limit" message with the actual error pulled
	// from the logs. Without this, the dashboard shows a useless
	// blanket message and the user has to spelunk pod logs (which
	// are gone after helm uninstalls the build) to find out what
	// went wrong.
	if phase == "failed" {
		// Priority: kubelet's terminated reason > log-pattern match.
		// The kubelet sees signals + OOM kills the application stream
		// can't observe; trust it first.
		reason := terminatedReason
		if reason == "" {
			reason = extractFailureReason(logs)
		}
		if reason != "" {
			patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, annMessage, reason)
			if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
				Patch(lctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
				slog.Default().Warn("builds: patch failure reason", "err", err, "build", b.Name)
			}
			// Re-stamp the durable BuildRecord with the REFINED message.
			// queueArchive already wrote a record synchronously (via
			// archiveRecord) reading annMessage BEFORE this refinement
			// existed — so it holds the generic "backoff limit" message.
			// Without this re-save the Deployments-tab archived view would
			// forever show the generic cause even though the live CR (just
			// patched above) shows the good one. Update the local
			// annotation so archiveRecord's upsert carries the refined
			// message. Best-effort; a failure just leaves the generic
			// message in the record.
			if b.Annotations == nil {
				b.Annotations = map[string]string{}
			}
			b.Annotations[annMessage] = reason
			p.archiveRecord(lctx, b, phase)
		}
	}
}

// extractTerminatedReason inspects the build pod's containerStatuses
// for the kubelet's authoritative termination reason. Catches the
// failure modes that don't make it to stdout:
//
//   - OOMKilled — kernel killed the container; logs end mid-step
//   - Error with exit code 137 (SIGKILL, often OOM)
//   - Error with exit code 143 (SIGTERM, often eviction)
//   - Evicted — node ran out of disk / memory pressure
//
// Walks both LastTerminationState (when the container restarted) and
// State.Terminated (final state). Returns "" when no terminated
// container is found, letting the log scan take over.
//
// We format with the exit code when present so a user can grep for
// "exit 137" → OOM, "exit 1" → app error, etc.
func extractTerminatedReason(pods []corev1.Pod) string {
	for i := range pods {
		pod := &pods[i]
		// Prefer the main containers; init container failures usually
		// already wrote a clear reason to stdout (git fatal, etc.).
		for _, allStatuses := range [][]corev1.ContainerStatus{
			pod.Status.ContainerStatuses,
			pod.Status.InitContainerStatuses,
		} {
			for _, cs := range allStatuses {
				if t := terminatedFromState(cs); t != "" {
					return t
				}
			}
		}
		// Pod-level status reason (Evicted, etc.) covers cases where
		// containers never started — disk-pressure eviction, kubelet
		// rejecting a too-large request.
		if pod.Status.Reason != "" && pod.Status.Phase == corev1.PodFailed {
			return "pod " + pod.Status.Reason + ": " + pod.Status.Message
		}
	}
	return ""
}

// extractTerminatedSignal returns the structured (reason, exitCode) for
// the build pod's failing container, for feeding failures.Classify. This
// is the machine-readable sibling of extractTerminatedReason (which
// produces the human message). Without it the build-failure classifier
// runs on log lines ALONE — so an OOMKilled build (kernel kills the
// process mid-step, often with no telltale log line) classifies as a
// generic "see logs" instead of KindOOM with a "bump memory" CTA.
// Returns ("", 0) when no container terminated abnormally.
func extractTerminatedSignal(pods []corev1.Pod) (string, int32) {
	for i := range pods {
		pod := &pods[i]
		for _, allStatuses := range [][]corev1.ContainerStatus{
			pod.Status.ContainerStatuses,
			pod.Status.InitContainerStatuses,
		} {
			for _, cs := range allStatuses {
				t := cs.State.Terminated
				if t == nil {
					t = cs.LastTerminationState.Terminated
				}
				if t == nil || t.ExitCode == 0 {
					continue
				}
				return t.Reason, t.ExitCode
			}
		}
		if pod.Status.Reason != "" && pod.Status.Phase == corev1.PodFailed {
			return pod.Status.Reason, 0
		}
	}
	return "", 0
}

func terminatedFromState(cs corev1.ContainerStatus) string {
	t := cs.State.Terminated
	if t == nil {
		t = cs.LastTerminationState.Terminated
	}
	if t == nil || t.ExitCode == 0 {
		return ""
	}
	switch t.Reason {
	case "OOMKilled":
		return fmt.Sprintf("OOMKilled — build hit memory limit (exit %d). Increase Settings → Builds → memory limit, or reduce build footprint.", t.ExitCode)
	case "Error":
		// Bare "Error" is a non-zero exit; format with code so users
		// can map 137 → SIGKILL, 143 → SIGTERM, 1 → app error, etc.
		return fmt.Sprintf("container exited with code %d (%s)", t.ExitCode, cs.Name)
	case "":
		return fmt.Sprintf("container exited with code %d (%s)", t.ExitCode, cs.Name)
	default:
		return fmt.Sprintf("%s (exit %d, container %s)", t.Reason, t.ExitCode, cs.Name)
	}
}

// extractFailureReason scans build logs for the most useful single-
// line summary of why the build failed. Pattern-matches the four
// failure shapes that account for ~95% of real-world cases:
//
//   - git clone errors (private repo without auth, branch not found,
//     repository moved/deleted)
//   - kaniko/buildpacks "build step N: failed" lines
//   - Dockerfile / nix syntax errors (the parser emits a clear
//     "ERROR: parse error" line)
//   - registry push failures (unauthorized, quota exceeded)
//
// Returns "" when no specific reason is found — caller falls back to
// the kube Job condition message. Bounded at 256 chars so a runaway
// stack trace doesn't bloat the build CR's annotations beyond the
// kube etcd 1MiB-per-object soft limit.
func extractFailureReason(logs string) string {
	if logs == "" {
		return ""
	}
	patterns := []*regexp.Regexp{
		// git clone failures
		regexp.MustCompile(`(?m)^.*fatal: (?:repository .+ not found|could not read Username|Authentication failed|.+ not exists|.+ unable to access).*$`),
		regexp.MustCompile(`(?m)^.*Repository not found.*$`),
		regexp.MustCompile(`(?m)^.*remote: Invalid username or password.*$`),
		regexp.MustCompile(`(?m)^.*ERROR: GITHUB_INSTALLATION_TOKEN must be set.*$`),
		// kaniko / buildpacks
		regexp.MustCompile(`(?m)^error building image:.*$`),
		regexp.MustCompile(`(?m)^.*ERROR: failed to build:.*$`),
		regexp.MustCompile(`(?m)^.*executor failed running.*: exit code: \d+.*$`),
		// Dockerfile / nixpacks parse
		regexp.MustCompile(`(?m)^.*Dockerfile parse error.*$`),
		regexp.MustCompile(`(?m)^.*nixpacks .* failed.*$`),
		// registry push
		regexp.MustCompile(`(?m)^.*denied: requested access to the resource is denied.*$`),
		regexp.MustCompile(`(?m)^.*push failed:.*$`),
	}
	for _, re := range patterns {
		if m := re.FindString(logs); m != "" {
			m = strings.TrimSpace(m)
			if len(m) > 256 {
				m = m[:256]
			}
			return m
		}
	}
	return ""
}

// detectedEnvRe matches the env-detect init container's sentinel-fenced
// JSON block. The init writes the block in one go so we don't need to
// reassemble across ANSI/timestamp prefixes.
var detectedEnvRe = regexp.MustCompile(`(?s)KUSO_ENV_DETECT_BEGIN\s*\n(\[.*?\])\s*\nKUSO_ENV_DETECT_END`)

// parseDetectedEnv extracts the var-name list from build logs. Returns
// nil when the sentinel isn't present (e.g. older build pod, build
// failed before env-detect ran). The list is JSON-decoded so a malformed
// block is just dropped rather than corrupting the build status.
func parseDetectedEnv(logs string) []string {
	m := detectedEnvRe.FindStringSubmatch(logs)
	if len(m) < 2 {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(m[1]), &names); err != nil {
		return nil
	}
	// Empty list is meaningful (= scan ran, found nothing); preserve it.
	if names == nil {
		names = []string{}
	}
	return names
}

// persistDetectedEnv stamps the parsed list onto KusoBuild's
// annotations. Annotations (not .status) because helm-operator owns
// the status subresource on these CRs and would clobber a write race.
// Same pattern used by the build-phase tracker (annPhase). The chart
// doesn't read this — it's purely consumed by the API surface for the
// EnvVarsEditor's "missing vars" suggestion.
//
// Encoding: JSON-serialised array under
// `kuso.sislelabs.com/detected-env`, plus a sibling timestamp at
// `kuso.sislelabs.com/detected-env-at` so the UI can show "detected
// at <build trigger time>".
func (p *Poller) persistDetectedEnv(ctx context.Context, ns string, b *kube.KusoBuild, names []string) error {
	if p.Svc == nil || p.Svc.Kube == nil {
		return nil
	}
	if names == nil {
		names = []string{}
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return err
	}
	patch := []byte(fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q,%q:%q}}}`,
		annDetectedEnv, string(encoded),
		annDetectedEnvAt, time.Now().UTC().Format(time.RFC3339),
	))
	_, err = p.Svc.Kube.Dynamic.
		Resource(kube.GVRBuilds).
		Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// containerNames extracts pod container names. Wrapped because the
// init+main split needs concatenating and a tiny helper makes
// archiveLogs readable.
func (p *Poller) markSucceeded(ctx context.Context, ns string, b *kube.KusoBuild) error {
	// Re-read the CR right before stamping. A user Cancel between
	// "kaniko exited 0" and this patch would otherwise be undone:
	// we'd flip phase=cancelled back to succeeded and the promote
	// path further down would push the cancelled image to prod.
	// Cancel writes phase=cancelled atomically; the lookup here
	// turns "user hit stop" into a no-op in this code path.
	if cur, gerr := p.Svc.Kube.GetKusoBuild(ctx, ns, b.Name); gerr == nil {
		if buildPhase(cur) == "cancelled" {
			p.logger().Info("build cancelled mid-flight; skipping promote", "build", b.Name)
			return nil
		}
	}
	// SEC-6: promote the image to the target env(s) BEFORE marking the
	// build terminal. Previously this order was inverted — we stamped
	// spec.done=true + build-state=done first and promoted last, so a
	// promotion failure (CAS exhausted, apiserver blip on the env
	// Update) left the build looking green in the UI while production
	// kept running the old image forever: observeNamespace skips
	// build-state=done builds, so checkBuild → markSucceeded →
	// promoteImage never re-ran. Now promotion runs first; only on
	// success do we stamp the terminal markers below. On failure we
	// return the error WITHOUT stamping done, so the build stays active
	// and the next poller tick retries the promotion (idempotent via
	// the last-trigger-wins guard). The narrow operator-resurrection
	// window that spec.done=true guards against is recoverable; a
	// silently-stranded production is not — so retriability wins.
	//
	// DryRun builds never push an image, so there's nothing to promote
	// and they're terminal immediately (handled after the stamp).
	if !b.Spec.DryRun {
		if err := p.promoteImage(ctx, ns, b); err != nil {
			p.logger().Error("build promote failed; leaving build non-terminal for retry",
				"build", b.Name, "ns", ns, "err", err)
			return fmt.Errorf("promote image (build stays retriable): %w", err)
		}
		// promoteImage may have set its own terminal phase — a failed
		// release hook stamps phase=release-failed + done=true via
		// markReleaseFailed and the image is deliberately NOT promoted.
		// In that case this build is already terminal; stamping
		// "succeeded" over it would both lie about the outcome and
		// resurrect the build for the operator. Re-read and bail if a
		// terminal non-succeeded phase was written.
		if cur, gerr := p.Svc.Kube.GetKusoBuild(ctx, ns, b.Name); gerr == nil {
			switch buildPhase(cur) {
			case "release-failed", "failed", "cancelled":
				p.logger().Info("build reached terminal non-succeeded phase during promote; not stamping succeeded",
					"build", b.Name, "phase", buildPhase(cur))
				return nil
			}
		}
	}
	// spec.done=true gates the kusobuild chart to render zero objects
	// from this point forward. Without it, an operator restart's
	// initial cache sync would helm-install a fresh release for this
	// CR (the build-state=done watch selector skips events but not
	// the startup re-sync) and resurrect the Job we already cleaned
	// up. We OOMKilled a 4 GB host once on this — two nixpacks
	// builds resurrected on top of each other.
	completedAt := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"succeeded",%q:%q},"labels":{"kuso.sislelabs.com/build-state":"done"}},"spec":{"done":true}}`,
		annPhase, annCompletedAt, completedAt,
	)
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch build status: %w", err)
	}
	// Mirror the stamp onto the in-memory object: archiveRecord below
	// reads b.Annotations, and this copy predates the patch — without
	// the mirror every archived record carried FinishedAt:"".
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	b.Annotations[annCompletedAt] = completedAt
	// Delete the per-build clone-token Secret immediately rather
	// than waiting for the Job TTL (1h) to clean it up. The Secret
	// carries a short-lived GitHub installation token; any pod with
	// secrets:get in the build namespace during that window can
	// clone the repo. Best-effort: kube errors are logged, never
	// block the status patch.
	p.deleteCloneTokenSecret(ns, b.Name)
	// Snapshot the pod logs BEFORE the kaniko Job's TTL reaper can
	// delete them. 1h is the chart's default ttlSecondsAfterFinished,
	// but a slow tick interleaved with a slow apiserver could miss
	// the window — taking the snapshot synchronously here costs ~1s
	// and removes that race.
	p.queueArchive(ctx, ns, b, "succeeded")
	if p.Notifier != nil {
		short := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
		// Best-effort site URL lookup for the "Site" field; failure
		// just omits the field.
		var siteURL string
		if p.Svc != nil {
			siteURL = lookupSiteURL(ctx, p.Svc.Kube, ns, b.Spec.Project, b.Spec.Service)
		}
		// Title shows the cosmetic displayName when set (slug fallback);
		// URLs + the Service field below stay the slug for deep-links.
		var kc *kube.Client
		if p.Svc != nil {
			kc = p.Svc.Kube
		}
		label := serviceDisplayLabel(ctx, kc, ns, b.Spec.Service, short)
		title, desc, fields := buildRichCard(b, label, "succeeded", "", siteURL)
		p.Notifier.Emit(EventEnvelope{
			Type:        eventBuildSucceeded,
			Title:       title,
			Description: desc,
			Project:     b.Spec.Project,
			Service:     short,
			URL:         buildEventURL(b.Spec.Project, short),
			Severity:    "info",
			DurationMs:  buildDurationMs(b),
			Fields:      fields,
		})
	}
	// Promotion already ran (and succeeded) above, before the terminal
	// stamp — see the SEC-6 block near the top of markSucceeded. DryRun
	// builds skipped it entirely (no image was pushed) but still emit
	// the success notification + log archive so the caller can confirm
	// the dry-run completed cleanly.
	if b.Spec.DryRun {
		p.logger().Info("dry-run build succeeded (no promote)", "build", b.Name)
	}
	return nil
}

func (p *Poller) markFailed(ctx context.Context, ns string, b *kube.KusoBuild, msg string) error {
	// Classify the failure ONCE, up front, so both the persisted build
	// record (read by the Deployments tab to render the hint + the
	// copy-pasteable remediation) and the notify card below share one
	// classification. Pull the last 50 lines — the classifier walks the
	// tail in reverse for a known failure regex; a 5-line window misses
	// errors that print 10-30 lines before the end of buildkit/nixpacks
	// output. Feed the pod's terminated reason/exit too: an OOMKilled
	// build leaves little in the logs (kernel kills it mid-step), so
	// reason="OOMKilled"/exit=137 is what reaches KindOOM/KindBuildOOM.
	//
	// Done BEFORE stamping phase=failed so a clone-ref-missing build can be
	// diverted to CANCELLED instead — the ref was deleted/force-pushed
	// while the build sat queued, nothing is actually broken, and a
	// build.failed here would page @here for a non-event.
	classifyLines := p.tailBuildLogLines(ctx, ns, b, 50)
	var sig failures.Signal
	if bp, perr := p.Svc.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: kube.LabelSelector(map[string]string{"app.kubernetes.io/instance": b.Name}),
	}); perr == nil {
		reason, exit := extractTerminatedSignal(bp.Items)
		sig.Reason, sig.ExitCode = reason, exit
	}
	classification := failures.Classify(classifyLines, sig)

	// Divert clone-of-deleted-ref failures to cancelled: no build.failed,
	// no @here. cancelBuild stamps phase=cancelled with a "ref deleted"
	// reason and emits build.cancelled at severity=info. On cancel error
	// (e.g. the build already went terminal in a race) fall through to the
	// normal failed path so the build never silently disappears.
	if classification.Kind == failures.KindCloneRefMissing {
		if cerr := p.Svc.cancelBuild(ctx, b.Spec.Project, b.Name, "ref deleted (branch deleted or force-pushed while build was queued)"); cerr == nil {
			// Persist the classification so an explicit `kuso build why
			// <id>` still explains a ref-deleted cancel (the auto-pick only
			// scans failed builds; this one is cancelled). Best-effort.
			if cj, mErr := json.Marshal(classification); mErr == nil {
				cPatch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, annClassification, string(cj))
				if _, pErr := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
					Patch(ctx, b.Name, types.MergePatchType, []byte(cPatch), metav1.PatchOptions{}); pErr != nil {
					p.logger().Warn("persist ref-missing classification", "build", b.Name, "err", pErr)
				}
			}
			p.deleteCloneTokenSecret(ns, b.Name)
			p.logger().Info("build cancelled: clone ref no longer exists",
				"build", b.Name, "branch", b.Spec.Branch, "project", b.Spec.Project)
			return nil
		} else {
			p.logger().Warn("clone-ref-missing cancel failed; falling through to failed",
				"build", b.Name, "err", cerr)
		}
	}

	// See markSucceeded for why spec.done=true is set — same operator
	// initial-cache-sync resurrection bug.
	completedAt := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"failed",%q:%q,%q:%q},"labels":{"kuso.sislelabs.com/build-state":"done"}},"spec":{"done":true}}`,
		annPhase, annCompletedAt, completedAt,
		annMessage, msg,
	)
	_, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch build failed: %w", err)
	}
	// Mirror onto the in-memory object for archiveRecord (see
	// markSucceeded — the stale copy is what gets archived).
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	b.Annotations[annCompletedAt] = completedAt
	// Same rationale as markSucceeded: drop the clone-token Secret
	// the moment the build is terminal instead of leaving it for the
	// Job TTL to harvest.
	p.deleteCloneTokenSecret(ns, b.Name)
	p.queueArchive(ctx, ns, b, "failed")
	// Persist the classification (incl. any actionable Remediation) on
	// the build CR so the Deployments tab can render the fix when the
	// user opens the failed build — not just in the bell popover. JSON
	// in an annotation keeps the wire shape intact across the build-
	// summary API + the durable archive. Best-effort: a failed marshal/
	// patch must not block the failure flow.
	if cj, mErr := json.Marshal(classification); mErr == nil {
		cPatch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, annClassification, string(cj))
		if _, pErr := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
			Patch(ctx, b.Name, types.MergePatchType, []byte(cPatch), metav1.PatchOptions{}); pErr != nil {
			p.logger().Warn("persist build classification", "build", b.Name, "err", pErr)
		}
	}

	if p.Notifier != nil {
		short := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
		// Discord-card tail is the last 5 — keep the card tight.
		logTail := joinLastN(classifyLines, 5)
		// Build-failed events never have a healthy site URL to link to
		// (the prior image is still live but unrelated). Build the
		// deep-link with ?tab=<hint> instead so the click lands the
		// user directly inside the service overlay.
		deepLink := buildFailureURL(b.Spec.Project, short, classification)
		var kc *kube.Client
		if p.Svc != nil {
			kc = p.Svc.Kube
		}
		label := serviceDisplayLabel(ctx, kc, ns, b.Spec.Service, short)
		title, desc, fields := buildRichCard(b, label, "failed", msg, "")
		p.Notifier.Emit(EventEnvelope{
			Type:           eventBuildFailed,
			Title:          title,
			Description:    desc,
			Body:           msg, // kept for back-compat sinks (raw webhook)
			LogTail:        logTail,
			Project:        b.Spec.Project,
			Service:        short,
			URL:            deepLink,
			Severity:       "error",
			DurationMs:     buildDurationMs(b),
			Fields:         fields,
			Classification: &classification,
		})
	}
	return nil
}

func (p *Poller) markRunning(ctx context.Context, ns string, b *kube.KusoBuild) error {
	if buildPhase(b) == "running" {
		return nil
	}
	// Stamp started-at the first time we see the kaniko Job pod active.
	// Without this the build-summary endpoint has no startedAt for the
	// running build, the deployments panel can't compute a live duration,
	// and the row shows just "12s ago" with no elapsed timer. Set it
	// once on the queued→running transition; markSucceeded/markFailed
	// don't touch it so finished builds keep their started-at intact.
	patch := []byte(fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"running",%q:%q}}}`,
		annPhase, annStartedAt, time.Now().UTC().Format(time.RFC3339),
	))
	_, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch build running: %w", err)
	}
	return nil
}

// logger returns the configured Logger or slog.Default(). Used so the
// poller's helper paths work in unit tests that construct a Poller
// directly without going through Run().
func (p *Poller) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// deleteCloneTokenSecret removes the per-build <name>-token Secret
// once the build is terminal. Best-effort + bounded: detaches from
// the caller's context (which may be about to return) so a slow
// apiserver doesn't block the build-marker patch; logs and swallows
// every error since the Job TTL is a fallback cleaner that still
// runs.
func (p *Poller) deleteCloneTokenSecret(ns, buildName string) {
	if p.Svc == nil || p.Svc.Kube == nil || p.Svc.Kube.Clientset == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		name := buildName + "-token"
		err := p.Svc.Kube.Clientset.CoreV1().Secrets(ns).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			p.logger().Warn("delete clone-token secret", "build", buildName, "ns", ns, "err", err)
		}
	}()
}

// annPromotedBuild + annPromotedAt stamp on the env CR record which
// build last won the promote race. A staler concurrent promote
// (Build A finished after Build B, but B was triggered later)
// otherwise silently shadows the newer image — under back-to-back
// pushes, production runs the older code. Compare-and-set on these
// annotations gives last-trigger-wins semantics regardless of which
// finishes first.
const (
	annPromotedBuild = "kuso.sislelabs.com/promoted-build"
	annPromotedAt    = "kuso.sislelabs.com/promoted-at"
)

// buildTriggerTimestamp returns the wall-clock the build was created
// at, fallback to "" if unset (older CRs). Used as the comparator for
// the promote-CAS check. CR creationTimestamp is monotonic per build
// pod within a single apiserver, which is good enough — we only need
// to order builds for the same service against each other.
func buildTriggerTimestamp(b *kube.KusoBuild) string {
	if !b.CreationTimestamp.IsZero() {
		return b.CreationTimestamp.UTC().Format(time.RFC3339Nano)
	}
	return ""
}

// promoteEnvImageCAS patches the build's image onto one env with real
// compare-and-set semantics on the promoted-at annotation. The naive
// "read from a List snapshot, then unconditionally MergePatch" pattern
// has a TOCTOU: two pollers (leader flip / brief double-leadership) or
// two builds finishing on back-to-back ticks both read the stale
// promoted-at, both pass the last-trigger-wins guard, then patch in
// arbitrary order — last writer by wall-clock wins, which can pin prod
// to the OLDER build's image. Here we re-Get the env, re-evaluate the
// guard against the FRESH annotation, and patch with a resourceVersion
// precondition so a concurrent write that moved promoted-at is rejected
// with 409; we retry and re-evaluate. Returns (patched, err): patched is
// false when the guard skipped the env (a newer trigger already won) or
// the env vanished.
func (p *Poller) promoteEnvImageCAS(ctx context.Context, ns, envName, bName, bTrigger string, img *kube.KusoImage) (bool, error) {
	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		raw, err := p.Svc.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			Get(ctx, envName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, fmt.Errorf("get env %s: %w", envName, err)
		}
		var e kube.KusoEnvironment
		if derr := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, &e); derr != nil {
			return false, fmt.Errorf("decode env %s: %w", envName, derr)
		}
		// Re-evaluate the last-trigger-wins guard against the fresh
		// annotation, not the stale List snapshot.
		if prev := e.Annotations[annPromotedAt]; prev != "" && bTrigger != "" && prev > bTrigger {
			p.logger().Info("build promote skipped (older trigger than current, CAS recheck)",
				"env", envName, "build", bName, "buildTrigger", bTrigger, "envPromotedAt", prev)
			return false, nil
		}
		// Mutate the freshly-read object in place and Update it. Update
		// carries the object's resourceVersion as an optimistic-concurrency
		// precondition: the apiserver returns 409 if the env changed since
		// our Get, which closes the TOCTOU the old unconditional MergePatch
		// had. On conflict we re-read and re-evaluate the guard.
		spec, _ := raw.Object["spec"].(map[string]any)
		if spec == nil {
			spec = map[string]any{}
			raw.Object["spec"] = spec
		}
		spec["image"] = map[string]any{
			"repository": img.Repository,
			"tag":        img.Tag,
			"pullPolicy": "IfNotPresent",
		}
		// Release the pre-build holding state: a build-based env is created
		// with replicaCount=0 (see projects.AddService) so it doesn't
		// crash-loop a placeholder ":latest" pod before the first image
		// exists. The first promote is the moment a real image lands, so
		// bump replicas off 0 here. Restore to the autoscaling floor when an
		// HPA owns scaling, else to 1; a user who explicitly scaled to 0
		// later (sleep) isn't affected because that path edits the env after
		// it already has an image (rc stays whatever they set on subsequent
		// promotes — we only lift the initial hold, i.e. rc==0 with no prior
		// promoted-build annotation).
		if e.Spec.ReplicaCount != nil && *e.Spec.ReplicaCount == 0 && e.Annotations[annPromotedBuild] == "" {
			restore := int64(1)
			if e.Spec.Autoscaling != nil && e.Spec.Autoscaling.Enabled && e.Spec.Autoscaling.MinReplicas > 0 {
				restore = int64(e.Spec.Autoscaling.MinReplicas)
			}
			spec["replicaCount"] = restore
		}
		meta, _ := raw.Object["metadata"].(map[string]any)
		anns, _ := meta["annotations"].(map[string]any)
		if anns == nil {
			anns = map[string]any{}
			meta["annotations"] = anns
		}
		anns[annPromotedBuild] = bName
		anns[annPromotedAt] = bTrigger
		if _, uerr := p.Svc.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			Update(ctx, raw, metav1.UpdateOptions{}); uerr != nil {
			if apierrors.IsConflict(uerr) {
				// Someone moved the env under us — re-read and re-evaluate.
				continue
			}
			if apierrors.IsNotFound(uerr) {
				return false, nil
			}
			return false, fmt.Errorf("promote-update env %s: %w", envName, uerr)
		}
		return true, nil
	}
	return false, fmt.Errorf("promote env %s: exhausted CAS retries (persistent conflict)", envName)
}

// promoteImage patches the new image tag onto every KusoEnvironment
// whose branch matches the build's branch. That's:
//
//   - the production env when builds are triggered against the project
//     default branch (whether by a push webhook or a manual `build trigger`);
//   - the preview-pr-N env when builds are triggered against a PR head
//     ref (via the dispatcher's onPullRequest path);
//   - both envs when somebody manually triggers a build with the same
//     branch as the PR they're testing — that's a no-op for production
//     in practice because webhook flows separate the two.
//
// The TS server only patched production; preview envs sat at
// InvalidImageName until a human manually edited spec.image. Per the
// rewrite plan §5 / TS comment in github-webhooks.service.ts, that was
// a known-incomplete feature. We close it here by matching on
// spec.branch over the env list filtered to this build's service.
func (p *Poller) promoteImage(ctx context.Context, ns string, b *kube.KusoBuild) error {
	if b.Spec.Image == nil {
		return nil
	}
	// List only envs that belong to this build's project + service.
	// Without the selector, every promotion walks every env in the
	// namespace — fine at 5 envs, painful at 50 (preview-env explosion
	// path).
	//
	// Env labels carry the SHORT service name (req.Name), but
	// b.Spec.Service is the FQN form "<project>-<service>". Strip the
	// project prefix so the selector matches the env's labels.
	// The in-memory `e.Spec.Service != b.Spec.Service` check below is
	// a belt-and-braces fallback for older envs missing the label.
	shortService := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
	if shortService == "" {
		shortService = b.Spec.Service
	}
	selector := kube.LabelSelector(map[string]string{
		kube.LabelProject: b.Spec.Project,
		kube.LabelService: shortService,
	})
	raw, err := p.Svc.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
		List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list envs for promotion: %w", err)
	}
	bTrigger := buildTriggerTimestamp(b)
	matched := 0
	// promoteFailed records whether promoting to ANY matched env hit a
	// hard error (CAS exhausted, apiserver blip on the env Update). We
	// do NOT abort the loop on the first such error — abandoning the
	// remaining envs would leave e.g. production promoted but staging
	// stranded on the old image with no retry. Instead we log, keep
	// going, and surface a non-nil error at the end so markSucceeded
	// declines to mark the build terminal and the next poller tick
	// retries the whole promotion (promoteEnvImageCAS is idempotent —
	// already-promoted envs skip via the last-trigger-wins guard).
	promoteFailed := false
	// releaseBlocked records whether a release hook (migration) failed or
	// errored for ANY of this build's source envs. When it did, the build's
	// image is unverified (migrations didn't apply), so we must NOT promote
	// it — neither to the source env (handled per-env via `continue` below)
	// NOR to fromService consumers (runtime=worker siblings). Without this
	// flag the second promote loop would push the un-migrated image to the
	// worker even though the api env correctly refused it.
	releaseBlocked := false
	for i := range raw.Items {
		var e kube.KusoEnvironment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &e); err != nil {
			continue
		}
		if e.Spec.Service != b.Spec.Service {
			continue
		}
		if b.Spec.Branch != "" && e.Spec.Branch != "" && e.Spec.Branch != b.Spec.Branch {
			continue
		}
		// Last-trigger-wins guard against back-to-back finishes.
		// If a younger build already promoted, skip — this build
		// would otherwise overwrite production with older code. This is
		// an early-out on the stale List snapshot; the authoritative
		// re-check happens under CAS in promoteEnvImageCAS below.
		if prev := e.Annotations[annPromotedAt]; prev != "" && bTrigger != "" && prev > bTrigger {
			p.logger().Info("build promote skipped (older trigger than current)",
				"env", e.Name, "build", b.Name, "buildTrigger", bTrigger, "envPromotedAt", prev)
			continue
		}
		// Release-hook gate: if the env carries a release command,
		// run it as a Job against the NEW image BEFORE patching the
		// env's image tag. On non-zero exit (or timeout) the build
		// is marked release-failed, no patch happens, existing pods
		// keep running on the previous image, and we emit a notify
		// event so the failure doesn't bury itself.
		//
		// Idempotency: the Job name is per-(env, image-tag), so a
		// re-deploy of the same tag is a no-op (Job exists, already
		// succeeded). See releaserun.JobName.
		if shouldRunRelease(&e) && p.ReleaseRunner != nil {
			res, rerr := p.ReleaseRunner.Run(ctx, ns, &e, b.Spec.Image)
			if rerr != nil {
				// Infra error talking to kube — log + skip this env
				// rather than promoting silently. The next poller
				// tick will retry. The image is unverified, so block
				// fromService propagation too.
				releaseBlocked = true
				p.logger().Error("release hook run failed (infra error)",
					"env", e.Name, "build", b.Name, "err", rerr)
				continue
			}
			if res.Outcome != releaserun.OutcomeSucceeded {
				releaseBlocked = true
				p.logger().Warn("release hook failed — skipping image promote",
					"env", e.Name, "build", b.Name, "outcome", res.Outcome, "job", res.JobName, "msg", res.Message)
				p.markReleaseFailed(ctx, ns, b, &e, res)
				continue
			}
			p.logger().Info("release hook succeeded",
				"env", e.Name, "build", b.Name, "job", res.JobName)
		}
		promoted, err := p.promoteEnvImageCAS(ctx, ns, e.Name, b.Name, bTrigger, b.Spec.Image)
		if err != nil {
			// Don't abort — log and keep promoting the other envs so a
			// transient error on one doesn't strand the rest on the old
			// image. The end-of-func error keeps the build retriable.
			promoteFailed = true
			p.logger().Error("promote env failed; continuing with remaining envs",
				"env", e.Name, "build", b.Name, "err", err)
			continue
		}
		if !promoted {
			continue
		}
		matched++
		p.logger().Info("build promoted", "env", e.Name, "ns", ns, "tag", b.Spec.Image.Tag, "build", b.Name)
	}
	if matched == 0 {
		p.logger().Warn("build succeeded but no env matched for promotion",
			"service", b.Spec.Service, "branch", b.Spec.Branch, "tag", b.Spec.Image.Tag)
	}

	// runtime=worker services use FromService to point at a sibling whose
	// image they reuse. When the sibling builds, those workers also need
	// the new tag — otherwise the worker env sits at the old image (or
	// the default empty image=":latest" InvalidImageName state on a
	// brand-new service). The first promotion loop above keyed by
	// e.Spec.Service so it can't reach the workers; this second loop
	// walks the same namespace and patches every env whose owning
	// service has FromService == shortService.
	//
	// BUT: only propagate when the source build's release hook(s) passed.
	// A release-failed build's image has un-applied migrations; pushing it
	// to a worker (which has no release hook of its own to gate on) would
	// run the worker against a schema the migration never created. The
	// worker stays on its previous image until a build's release succeeds.
	if releaseBlocked {
		p.logger().Warn("release hook blocked promotion — not propagating image to fromService consumers",
			"service", b.Spec.Service, "build", b.Name)
		// A blocked release means the image is intentionally NOT promoted
		// yet; that's not a promotion failure to retry here (markReleaseFailed
		// already handled the source env). Report success so the build is
		// marked terminal — the release-failed annotation is the record.
		return nil
	}
	if err := p.promoteToFromServiceConsumers(ctx, ns, b, shortService, bTrigger); err != nil {
		// Workers being stale is a real bug but not worth failing the
		// whole promotion over — the api/web env did get patched and
		// users see traffic. Log and move on; the next reconcile picks
		// up the worker.
		p.logger().Warn("promote to fromService consumers failed",
			"service", b.Spec.Service, "err", err)
	}
	if promoteFailed {
		// At least one matched env failed to promote. Return an error so
		// markSucceeded does NOT stamp the build terminal — the next
		// poller tick re-enters checkBuild → markSucceeded → promoteImage
		// and retries the failed env(s). Idempotent for the ones that
		// already landed.
		return fmt.Errorf("one or more envs failed to promote for build %s", b.Name)
	}
	return nil
}

// promoteToFromServiceConsumers patches the image tag onto every env
// owned by a service whose Spec.FromService matches the just-built
// service. This is the runtime=worker / fromService pattern: the
// worker has no repo of its own and inherits the sibling's image.
//
// Walks the namespace's KusoServices first to find names with the
// matching FromService field, then walks envs filtered by that
// service. Branch and last-trigger guards mirror the main promote
// loop so a worker doesn't get pinned to an older tag.
func (p *Poller) promoteToFromServiceConsumers(ctx context.Context, ns string, b *kube.KusoBuild, sourceShortName, bTrigger string) error {
	if b.Spec.Image == nil {
		return nil
	}
	// List every KusoService in the namespace. At the scale of one
	// kuso install (single-team) this is dozens of services, not
	// thousands; an unindexed scan is fine and avoids a labels-on-CR
	// migration just for this lookup.
	rawSvcs, err := p.Svc.Kube.Dynamic.Resource(kube.GVRServices).Namespace(ns).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	for i := range rawSvcs.Items {
		var s kube.KusoService
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rawSvcs.Items[i].Object, &s); err != nil {
			continue
		}
		if s.Spec.FromService != sourceShortName {
			continue
		}
		// Find this service's envs and patch each.
		workerSelector := kube.LabelSelector(map[string]string{
			kube.LabelProject: b.Spec.Project,
			kube.LabelService: strings.TrimPrefix(s.Name, b.Spec.Project+"-"),
		})
		rawEnvs, err := p.Svc.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			List(ctx, metav1.ListOptions{LabelSelector: workerSelector})
		if err != nil {
			p.logger().Warn("list worker envs failed", "service", s.Name, "err", err)
			continue
		}
		for j := range rawEnvs.Items {
			var e kube.KusoEnvironment
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rawEnvs.Items[j].Object, &e); err != nil {
				continue
			}
			if b.Spec.Branch != "" && e.Spec.Branch != "" && e.Spec.Branch != b.Spec.Branch {
				continue
			}
			if prev := e.Annotations[annPromotedAt]; prev != "" && bTrigger != "" && prev > bTrigger {
				continue
			}
			promoted, err := p.promoteEnvImageCAS(ctx, ns, e.Name, b.Name, bTrigger, b.Spec.Image)
			if err != nil {
				p.logger().Warn("promote worker env failed", "env", e.Name, "err", err)
				continue
			}
			if !promoted {
				continue
			}
			p.logger().Info("worker env promoted via fromService",
				"env", e.Name, "fromService", sourceShortName, "tag", b.Spec.Image.Tag)
		}
	}
	return nil
}

// ReleaseRunner is the interface the build poller calls into for the
// pre-deploy release Job. Defined locally so the builds package
// doesn't import releaserun directly — keeps the import graph one-
// way (main.go wires the concrete type in).
type ReleaseRunner interface {
	Run(ctx context.Context, ns string, env *kube.KusoEnvironment, image *kube.KusoImage) (releaserun.Result, error)
}

// shouldRunRelease reports whether the build poller should run the pre-deploy
// release Job for this env. It runs for any env carrying a release command —
// EXCEPT preview envs. Preview migrations are owned by the seed path
// (previewdb runs the migration AFTER the per-PR clone is (re)seeded, on every
// PR lifecycle event including close→reopen). If the poller also ran the
// release Job for previews it would race the seed (which `pg_dump --clean`s the
// DB after promote) and trip the per-(env,tag) idempotency on reopen, leaving
// the preview un-migrated. Production and other long-lived envs have no seed,
// so the poller stays their migration trigger.
func shouldRunRelease(e *kube.KusoEnvironment) bool {
	if e.Spec.Release == nil || len(e.Spec.Release.Command) == 0 {
		return false
	}
	if e.Spec.Kind == "preview" {
		return false
	}
	return true
}

// markReleaseFailed stamps the build CR with phase=release-failed and
// a human-readable message, and fires a notify event so the failure
// is surfaced through the bell + webhook channels instead of buried
// in the deployments tab. Best-effort: a kube write failure here only
// affects the surfacing, not the gate (the image tag is still NOT
// patched).
func (p *Poller) markReleaseFailed(ctx context.Context, ns string, b *kube.KusoBuild, e *kube.KusoEnvironment, res releaserun.Result) {
	now := time.Now().UTC().Format(time.RFC3339)
	msg := res.Message
	if msg == "" {
		msg = string(res.Outcome)
	}
	// Stamp the build with the failure so the UI's deployments tab can
	// render "Release failed" + a deep-link to the Job logs without
	// inferring it from the env CR.
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"release-failed",%q:%q,%q:%q,%q:%q},"labels":{"kuso.sislelabs.com/build-state":"done"}},"spec":{"done":true}}`,
		annPhase,
		annCompletedAt, now,
		annMessage, msg,
		"kuso.sislelabs.com/release-job", res.JobName,
	)
	if _, perr := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
		p.logger().Warn("mark release-failed: patch build", "err", perr, "build", b.Name)
	}
	if p.Notifier != nil {
		short := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
		label := serviceDisplayLabel(ctx, p.Svc.Kube, ns, b.Spec.Service, short)
		title := fmt.Sprintf("Release hook failed for %s/%s", b.Spec.Project, label)
		desc := fmt.Sprintf("release command exited with %s — image was NOT promoted, existing pods unchanged. job=%s", res.Outcome, res.JobName)
		fields := map[string]string{
			"env":     e.Name,
			"image":   fmt.Sprintf("%s:%s", b.Spec.Image.Repository, b.Spec.Image.Tag),
			"job":     res.JobName,
			"outcome": string(res.Outcome),
		}
		// Reuse the eventBuildFailed channel so existing webhook
		// subscribers pick this up — release failures are a flavour
		// of build-failure as far as alert routing is concerned.
		p.Notifier.Emit(EventEnvelope{
			Type:        eventBuildFailed,
			Title:       title,
			Description: desc,
			Project:     b.Spec.Project,
			Service:     short,
			URL:         buildEventURL(b.Spec.Project, short),
			Severity:    "warning",
			Fields:      flattenFields(fields),
		})
	}
}

// flattenFields converts a map[string]string into the []EnvelopeField
// shape the notify dispatcher expects. Tiny helper; lives here because
// markReleaseFailed is the only caller.
func flattenFields(m map[string]string) []EnvelopeField {
	if len(m) == 0 {
		return nil
	}
	out := make([]EnvelopeField, 0, len(m))
	for k, v := range m {
		out = append(out, EnvelopeField{Name: k, Value: v, Inline: true})
	}
	return out
}

// asUnstructured is a small helper to build the unstructured shape for
// writing builds in tests.
func asUnstructured(b *kube.KusoBuild) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(b)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRBuilds.GroupVersion().WithKind("KusoBuild"))
	return u, nil
}

// Used by tests under the same package to build seed objects without
// re-importing kube internals.
var _ = asUnstructured
