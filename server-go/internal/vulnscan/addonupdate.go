package vulnscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/client-go/kubernetes"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

// Addon images are upstream tags ("postgres:16") that nodes pull once
// and then run forever: the chart's IfNotPresent pull policy means a
// restart never picks up upstream's security rebuilds. The updater
// closes that gap. Per addon it resolves the tag's current digest,
// scans that digest, and — inside the nightly window, when the new
// image has no more fixable critical+high findings than the running
// one — stamps spec.imagePin so the chart renders tag@digest and the
// addon rolls onto it. The tag (so the major/minor) never changes.

const (
	addonUpdateStateKey = "vulnscan.addonImageUpdates"
	// updateWindow is how long after nightlyHourUTC rolls may start.
	updateWindow = 4 * time.Hour
	// maxRollsPerTick spaces restarts out (a tick is 10 minutes).
	maxRollsPerTick = 2
	// rollbackAfter: pods not Ready on the pinned image this long after
	// the pin get pinned back to the digest they ran before.
	rollbackAfter = 30 * time.Minute
)

// Update statuses, shown on the addon's Vulnerabilities tab.
const (
	UpdateCurrent    = "current"     // running the tag's newest image
	UpdateScanning   = "scanning"    // newer image found, scan pending
	UpdateReady      = "ready"       // newer image scanned, rolls in the window
	UpdateRejected   = "rejected"    // newer image scans worse; not rolled
	UpdateScanFailed = "scan-failed" // newer image couldn't be scanned
	UpdateUpdating   = "updating"    // pre-pulling / restarting right now
	UpdateRolling    = "rolling"     // pinned, waiting for pods to pick it up
	UpdateRolledBack = "rolled-back" // new image never got Ready; old one restored
	UpdateError      = "error"       // couldn't resolve or pin
)

// AddonUpdateState is what the updater knows about one addon.
type AddonUpdateState struct {
	Image     string    `json:"image"`             // tag ref the chart renders
	Running   string    `json:"running,omitempty"` // digest the pods run
	Latest    string    `json:"latest,omitempty"`  // digest the tag points at
	CheckedAt time.Time `json:"checkedAt"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	// Fixable critical+high of the running / newest image; -1 unknown.
	FixableRunning int        `json:"fixableRunning"`
	FixableLatest  int        `json:"fixableLatest"`
	AppliedAt      *time.Time `json:"appliedAt,omitempty"`
	// StartedAt is when the current "updating" run began; a run older
	// than updatingStale is treated as dead (server restarted mid-roll).
	StartedAt *time.Time `json:"startedAt,omitempty"`
	Manual    bool       `json:"manual,omitempty"`
	// RolledBack is a digest that was pinned and never got Ready; it is
	// not tried again until the tag moves on.
	RolledBack string `json:"rolledBack,omitempty"`
}

// AddonStore reads addons and stamps image pins.
type AddonStore interface {
	ListAddons(ctx context.Context) ([]kube.KusoAddon, error)
	PinAddonImage(ctx context.Context, namespace, name string, pin kube.KusoAddonImagePin) error
}

// TagResolver returns the digest a tag points at now.
type TagResolver interface {
	LatestDigest(ctx context.Context, ref string) (string, error)
}

// KubeAddons is the AddonStore over the kube client.
type KubeAddons struct{ Kube *kube.Client }

func (k KubeAddons) ListAddons(ctx context.Context) ([]kube.KusoAddon, error) {
	return k.Kube.ListKusoAddons(ctx, "")
}

func (k KubeAddons) PinAddonImage(ctx context.Context, namespace, name string, pin kube.KusoAddonImagePin) error {
	_, err := k.Kube.UpdateKusoAddonWithRetry(ctx, namespace, name, func(a *kube.KusoAddon) error {
		p := pin
		a.Spec.ImagePin = &p
		return nil
	})
	return err
}

// AddonUpdater rolls addons onto newer images of their tag.
type AddonUpdater struct {
	DB       *db.DB
	Cluster  Cluster
	Addons   AddonStore
	Resolver TagResolver
	Notify   Emitter
	Logger   *slog.Logger
	Now      func() time.Time
	// Kube + Namespace enable the low-downtime restart (pre-pull Jobs in
	// Namespace, Postgres checkpoint, PgBouncer pause). Nil Kube = pin only.
	Kube      kubernetes.Interface
	Namespace string
	// Launch runs a rollout in the background; tests run it inline.
	Launch func(func())
	// Timings, overridable in tests (zero = default).
	RolloutWait time.Duration
	PollEvery   time.Duration
}

func (u *AddonUpdater) launch(f func()) {
	if u.Launch != nil {
		u.Launch(f)
		return
	}
	go f()
}

func (u *AddonUpdater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *AddonUpdater) log() *slog.Logger {
	if u.Logger != nil {
		return u.Logger
	}
	return slog.Default()
}

// MainImage finds the image an addon's own pods run: the first
// container of its StatefulSet/Deployment/CNPG instance pods, skipping
// the pooler and Job pods (backups, bucket init) that share its label.
// Returns the tag ref without any digest and the digest the pods run.
// running is "" unless every such pod's main container is Ready on one
// digest (mid-roll, crash-looping, image pull failing). ref is "" when
// there is nothing to update (no pods, or pods disagree on the tag).
func MainImage(pods []corev1.Pod, a kube.KusoAddon) (ref, running string) {
	refs, digests := map[string]bool{}, map[string]bool{}
	unready := false
	for _, p := range mainPods(pods, a) {
		if p.DeletionTimestamp != nil {
			continue
		}
		c := p.Spec.Containers[0]
		r := c.Image
		if at := strings.IndexByte(r, '@'); at >= 0 {
			r = r[:at]
		}
		refs[r] = true
		ready := false
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name == c.Name && cs.Ready {
				if d := digestOf(cs.ImageID); d != "" {
					digests[d] = true
					ready = true
				}
			}
		}
		unready = unready || !ready
	}
	if len(refs) != 1 {
		return "", ""
	}
	for r := range refs {
		ref = r
	}
	if len(digests) == 1 && !unready {
		for d := range digests {
			running = d
		}
	}
	return ref, running
}

func inUpdateWindow(now time.Time, hourUTC int) bool {
	start := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), hourUTC, 0, 0, 0, time.UTC)
	if start.After(now) {
		start = start.AddDate(0, 0, -1)
	}
	return now.Sub(start) < updateWindow
}

func stateKey(a kube.KusoAddon) string { return a.Namespace + "/" + a.Name }

// LoadAddonUpdateStates reads the persisted per-addon state.
func LoadAddonUpdateStates(ctx context.Context, d *db.DB) (map[string]AddonUpdateState, error) {
	raw, err := d.GetSetting(ctx, addonUpdateStateKey)
	if err != nil {
		return nil, err
	}
	out := map[string]AddonUpdateState{}
	if raw == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]AddonUpdateState{}, nil // garbled state just means "check again"
	}
	return out, nil
}

// AddonUpdateStateFor returns one addon's state, or nil.
func AddonUpdateStateFor(ctx context.Context, d *db.DB, a kube.KusoAddon) (*AddonUpdateState, error) {
	all, err := LoadAddonUpdateStates(ctx, d)
	if err != nil {
		return nil, err
	}
	if st, ok := all[stateKey(a)]; ok {
		return &st, nil
	}
	return nil, nil
}

func fixable(s *db.VulnScan) int {
	if s == nil {
		return -1
	}
	return s.Counts.FixableCritical + s.Counts.FixableHigh
}

// updatingStale: an "updating" run older than this died with its
// server; the next tick picks the addon up again.
const updatingStale = 15 * time.Minute

func inFlight(st AddonUpdateState, now time.Time) bool {
	return st.Status == UpdateUpdating && st.StartedAt != nil && now.Sub(*st.StartedAt) < updatingStale
}

// Tick checks every addon once. Leader-only (runs inside the sweep).
// With auto-update off it only watches rolls already in flight (a
// manual "update now"), so those still get the rollback.
func (u *AddonUpdater) Tick(ctx context.Context, settings db.VulnScanSettings) error {
	auto := settings.AutoUpdateAddons && settings.ScanAddons && settings.Mode != ModeOff
	addons, err := u.Addons.ListAddons(ctx)
	if err != nil {
		return fmt.Errorf("list addons: %w", err)
	}
	pods, err := u.Cluster.Pods(ctx)
	if err != nil {
		return err
	}
	states, err := LoadAddonUpdateStates(ctx, u.DB)
	if err != nil {
		return err
	}
	now := u.now()
	window := inUpdateWindow(now, settings.NightlyHourUTC)
	sort.Slice(addons, func(i, j int) bool { return stateKey(addons[i]) < stateKey(addons[j]) })
	resolved := map[string]string{} // ref → digest, one lookup per tag per tick
	next := map[string]AddonUpdateState{}
	var rollouts []func()
	for _, a := range addons {
		if a.Spec.External != nil || a.Spec.UseInstanceAddon != "" {
			continue
		}
		key := stateKey(a)
		if st, ok := states[key]; ok && inFlight(st, now) {
			continue // its rollout owns the state
		}
		ref, running := MainImage(pods, a)
		if ref == "" {
			if st, ok := states[key]; ok {
				next[key] = st // asleep or mid-restart: keep what we knew
			}
			continue
		}
		st, known := states[key]
		if st.Image != ref {
			st, known = AddonUpdateState{Image: ref}, false // new addon or version change
		}
		if !auto {
			if known {
				next[key] = u.watchRoll(ctx, a, st, running, now)
			}
			continue
		}
		st, roll := u.step(ctx, a, st, ref, running, now, resolved)
		st = u.watchRoll(ctx, a, st, running, now)
		if roll && window && len(rollouts) < maxRollsPerTick {
			t := now
			st.Status, st.Message, st.StartedAt, st.Manual = UpdateUpdating, "pulling the new image", &t, false
			a, st := a, st
			rollouts = append(rollouts, func() { u.rollout(a, st) })
		}
		next[key] = st
	}
	if err := u.saveTick(ctx, next); err != nil {
		return err
	}
	for _, f := range rollouts {
		u.launch(f)
	}
	return nil
}

// step advances one addon's state. roll=true means the newest image is
// scanned, no worse than the running one, and not yet pinned.
func (u *AddonUpdater) step(ctx context.Context, a kube.KusoAddon, st AddonUpdateState, ref, running string, now time.Time, resolved map[string]string) (AddonUpdateState, bool) {
	if running != "" {
		st.Running = running
	}
	if st.Latest == "" || now.Sub(st.CheckedAt) >= ReuseWindow {
		d, ok := resolved[ref]
		if !ok {
			var err error
			if d, err = u.Resolver.LatestDigest(ctx, ref); err != nil {
				u.log().Warn("addon update: resolve tag", "image", ref, "err", err)
				st.Status, st.Message = UpdateError, err.Error()
				return st, false
			}
			resolved[ref] = d
		}
		st.Latest, st.CheckedAt = d, now
	}
	pinned := a.Spec.ImagePin != nil && a.Spec.ImagePin.Image == ref && a.Spec.ImagePin.Digest == st.Latest
	switch {
	case running != "" && running == st.Latest:
		st.Status, st.Message, st.RolledBack = UpdateCurrent, "", ""
		return st, false
	case st.Latest == st.RolledBack:
		st.Status = UpdateRolledBack
		return st, false
	case pinned:
		st.Status, st.Message = UpdateRolling, "waiting for pods to become Ready on the pinned image"
		if st.AppliedAt == nil {
			t := now // the rollout that pinned it died before saving
			st.AppliedAt = &t
		}
		return st, false
	case running == "":
		st.Message = "pods are restarting or not Ready"
		return st, false
	}

	scans, err := u.DB.LatestVulnScans(ctx, []string{st.Latest})
	if err != nil {
		st.Status, st.Message = UpdateError, err.Error()
		return st, false
	}
	cand, ok := scans[st.Latest]
	switch {
	case !ok || (cand.Status == db.VulnStatusFailed && now.Sub(cand.FinishedAt) >= ReuseWindow):
		_, _, err := u.DB.EnqueueVulnScan(ctx, db.VulnScanRequest{Digest: st.Latest, ImageRef: ImageRepo(ref) + "@" + st.Latest, Reason: db.VulnReasonNightly})
		if err != nil {
			st.Status, st.Message = UpdateError, err.Error()
		} else {
			st.Status, st.Message = UpdateScanning, ""
		}
		return st, false
	case cand.Status == db.VulnStatusQueued || cand.Status == db.VulnStatusRunning:
		st.Status, st.Message = UpdateScanning, ""
		return st, false
	case cand.Status == db.VulnStatusFailed:
		st.Status, st.Message = UpdateScanFailed, cand.Error
		return st, false
	}
	cur, err := u.DB.LatestSucceededVulnScans(ctx, []string{running})
	if err != nil {
		st.Status, st.Message = UpdateError, err.Error()
		return st, false
	}
	var curScan *db.VulnScan
	if c, ok := cur[running]; ok {
		curScan = &c
	}
	st.FixableRunning, st.FixableLatest = fixable(curScan), fixable(&cand)
	if st.FixableRunning >= 0 && st.FixableLatest > st.FixableRunning {
		st.Status = UpdateRejected
		st.Message = fmt.Sprintf("newest image has more fixable critical+high findings (%d) than the running one (%d)", st.FixableLatest, st.FixableRunning)
		return st, false
	}
	st.Status, st.Message = UpdateReady, ""
	return st, true
}

// watchRoll settles a roll in flight: done once the pods are Ready on
// the new digest, rolled back once rollbackAfter passes without that.
func (u *AddonUpdater) watchRoll(ctx context.Context, a kube.KusoAddon, st AddonUpdateState, running string, now time.Time) AddonUpdateState {
	if st.Status != UpdateRolling || st.AppliedAt == nil {
		return st
	}
	if running != "" && running == st.Latest {
		st.Status, st.Message, st.Running = UpdateCurrent, "", running
		return st
	}
	if now.Sub(*st.AppliedAt) >= rollbackAfter {
		return u.rollback(ctx, a, st)
	}
	return st
}

// rollback pins the digest the addon ran before the update. The old
// image is still in the node's cache, so this works offline too.
func (u *AddonUpdater) rollback(ctx context.Context, a kube.KusoAddon, st AddonUpdateState) AddonUpdateState {
	failed := st.Latest
	if st.Running == "" || st.Running == failed {
		st.Status, st.Message = UpdateError, "pods aren't Ready on the new image and the previous digest is unknown; check the addon's pods"
		return st
	}
	if err := u.pin(ctx, a, st.Image, st.Running); err != nil {
		st.Status, st.Message = UpdateError, "roll back: "+err.Error()
		return st
	}
	u.log().Warn("addon update: rolled back", "addon", a.Name, "image", st.Image, "failed", failed, "restored", st.Running)
	st.Status, st.RolledBack, st.AppliedAt = UpdateRolledBack, failed, nil
	st.Message = fmt.Sprintf("pods weren't Ready %s after moving to %s; restored the previous image", rollbackAfter, shortDigest(failed))
	if u.Notify != nil {
		ev := notify.AddonImageRolledBack(a.Spec.Project, strings.TrimPrefix(a.Name, a.Spec.Project+"-"), st.Image, failed, st.Message)
		if err := u.Notify.EmitDurable(ev); err != nil {
			u.log().Warn("addon update: notify", "addon", a.Name, "err", err)
		}
	}
	return st
}

func shortDigest(d string) string {
	if len(d) > len("sha256:")+12 {
		return d[:len("sha256:")+12]
	}
	return d
}

func (u *AddonUpdater) pin(ctx context.Context, a kube.KusoAddon, ref, digest string) error {
	return u.Addons.PinAddonImage(ctx, a.Namespace, a.Name, kube.KusoAddonImagePin{Image: ref, Digest: digest})
}

func (u *AddonUpdater) emit(a kube.KusoAddon, st AddonUpdateState, manual bool) {
	if u.Notify == nil {
		return
	}
	project := a.Spec.Project
	ev := notify.AddonImageUpdated(project, strings.TrimPrefix(a.Name, project+"-"), st.Image, st.Latest, st.FixableRunning, st.FixableLatest, manual)
	if err := u.Notify.EmitDurable(ev); err != nil {
		u.log().Warn("addon update: notify", "addon", a.Name, "err", err)
	}
}

func (u *AddonUpdater) updateStates(ctx context.Context, fn func(map[string]AddonUpdateState)) error {
	return u.DB.UpdateSettingLocked(ctx, addonUpdateStateKey, "vulnscan", func(old string) (string, error) {
		states := map[string]AddonUpdateState{}
		if old != "" {
			_ = json.Unmarshal([]byte(old), &states) // garbled = start over
		}
		fn(states)
		raw, err := json.Marshal(states)
		return string(raw), err
	})
}

// saveTick replaces the stored states with the tick's view, except
// addons a rollout is working on right now: those keep their own state.
func (u *AddonUpdater) saveTick(ctx context.Context, next map[string]AddonUpdateState) error {
	now := u.now()
	return u.updateStates(ctx, func(states map[string]AddonUpdateState) {
		for k, st := range states {
			if _, ok := next[k]; !ok && !inFlight(st, now) {
				delete(states, k)
			}
		}
		for k, st := range next {
			if cur, ok := states[k]; ok && inFlight(cur, now) && st.Status != UpdateUpdating {
				continue
			}
			states[k] = st
		}
	})
}

func (u *AddonUpdater) saveOne(ctx context.Context, a kube.KusoAddon, st AddonUpdateState) {
	if err := u.updateStates(ctx, func(states map[string]AddonUpdateState) { states[stateKey(a)] = st }); err != nil {
		u.log().Warn("addon update: save state", "addon", a.Name, "err", err)
	}
}

// ErrAlreadyCurrent: the addon already runs its tag's newest image.
var ErrAlreadyCurrent = errors.New("already on the newest image")

// ErrUpdateInProgress: a rollout for this addon is already running.
var ErrUpdateInProgress = errors.New("an image update is already running for this addon")

// UpdateNow starts moving one addon onto its tag's newest image now,
// skipping the window and the scan comparison. The rollout (pre-pull,
// restart, pooler pause) runs in the background; the returned state is
// "updating" and the addon's state shows how it went.
func (u *AddonUpdater) UpdateNow(ctx context.Context, a kube.KusoAddon) (AddonUpdateState, error) {
	if a.Spec.External != nil || a.Spec.UseInstanceAddon != "" {
		return AddonUpdateState{}, fmt.Errorf("%w: kuso doesn't run an image for this addon", ErrInvalid)
	}
	pods, err := u.Cluster.Pods(ctx)
	if err != nil {
		return AddonUpdateState{}, err
	}
	ref, running := MainImage(pods, a)
	if ref == "" {
		return AddonUpdateState{}, fmt.Errorf("%w: no running pods to update", ErrInvalid)
	}
	if running == "" {
		return AddonUpdateState{}, fmt.Errorf("%w: the addon's pods aren't all Ready; wait for them before updating", ErrInvalid)
	}
	latest, err := u.Resolver.LatestDigest(ctx, ref)
	if err != nil {
		return AddonUpdateState{}, err
	}
	if latest == running {
		return AddonUpdateState{}, fmt.Errorf("%w: %s", ErrAlreadyCurrent, ref)
	}
	now := u.now()
	st := AddonUpdateState{
		Image: ref, Running: running, Latest: latest, CheckedAt: now,
		Status: UpdateUpdating, Message: "pulling the new image", StartedAt: &now, Manual: true,
		FixableRunning: -1, FixableLatest: -1,
	}
	if cur, err := u.DB.LatestSucceededVulnScans(ctx, []string{running, latest}); err == nil {
		if c, ok := cur[running]; ok {
			st.FixableRunning = fixable(&c)
		}
		if c, ok := cur[latest]; ok {
			st.FixableLatest = fixable(&c)
		}
	}
	busy := false
	if err := u.updateStates(ctx, func(states map[string]AddonUpdateState) {
		if cur, ok := states[stateKey(a)]; ok && inFlight(cur, now) {
			busy = true
			return
		}
		states[stateKey(a)] = st
	}); err != nil {
		return AddonUpdateState{}, err
	}
	if busy {
		return AddonUpdateState{}, ErrUpdateInProgress
	}
	u.launch(func() { u.rollout(a, st) })
	return st, nil
}
