package vulnscan

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"time"

	"kuso/server/internal/db"
	"kuso/server/internal/notify"
	"kuso/server/internal/serverstate"
)

const (
	// SweepInterval is the nightly loop's tick (and liveness interval).
	// The sweep itself runs once a day; the tick only checks whether
	// it's due and alerts on finished scans.
	SweepInterval = 10 * time.Minute
	// ReuseWindow: a digest with a successful scan younger than this is
	// not rescanned (gate reuse and nightly skip alike).
	ReuseWindow = 20 * time.Hour
	// GoneAfter: inventory rows unseen this long no longer count as live.
	GoneAfter = 7 * 24 * time.Hour

	sweepEvery     = 20 * time.Hour
	retainPerImage = 5
	pruneGoneAfter = 30 * 24 * time.Hour
	tickBudget     = 5 * time.Minute

	lastSweepKey = "vulnscan.lastSweepAt"
	// baselineKey marks the first-ever sweep, whose results are a
	// baseline (one summary event, nothing per owner) rather than news.
	// "pending" while that sweep is queueing, then the highest scan id
	// it covers, then "done" once those scans have finished.
	baselineKey     = "vulnscan.baseline"
	baselinePending = "pending"
	baselineDone    = "done"
)

// Disabled is the KUSO_VULNSCAN_DISABLED kill switch. main.go gates
// both the loops and the heartbeat registration on it.
func Disabled() bool { return os.Getenv("KUSO_VULNSCAN_DISABLED") == "true" }

// Emitter is notify.Dispatcher's EmitDurable.
type Emitter interface {
	EmitDurable(notify.Event) error
}

// Sweeper runs the nightly inventory + rescan and turns finished scans
// into vulnerabilities_found alerts.
type Sweeper struct {
	DB      *db.DB
	Cluster Cluster
	Notify  Emitter
	Logger  *slog.Logger
	Now     func() time.Time
	// Updater, when set, rolls addons onto newer images of their tag
	// every tick (rolls only inside the nightly window).
	Updater *AddonUpdater

	// alertedThrough: scans finished before this were already looked
	// at by the alert pass in this process. Zero after a restart, which
	// costs one full pass; VulnNotified keeps it from re-alerting.
	alertedThrough time.Time
}

func (s *Sweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sweeper) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Run ticks until ctx ends, beating the liveness heartbeat every tick.
func (s *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
			serverstate.LoopHeartbeat(serverstate.LoopVulnScan)
		}
	}
}

// Tick runs the sweep when it's due, then the alert pass.
func (s *Sweeper) Tick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, tickBudget)
	defer cancel()
	settings, err := s.DB.GetVulnScanSettings(ctx)
	if err != nil {
		s.log().Warn("vulnscan: read settings", "err", err)
		return
	}
	if settings.Mode == ModeOff {
		return
	}
	last, err := s.lastSweep(ctx)
	if err != nil {
		s.log().Warn("vulnscan: read last sweep", "err", err)
		return
	}
	if sweepDue(s.now(), settings.NightlyHourUTC, last) {
		if n, err := s.Sweep(ctx); err != nil {
			s.log().Warn("vulnscan: nightly sweep", "err", err)
		} else {
			s.log().Info("vulnscan: nightly sweep queued scans", "queued", n)
		}
	}
	if err := s.AlertNew(ctx, settings); err != nil {
		s.log().Warn("vulnscan: alert pass", "err", err)
	}
	if s.Updater != nil {
		if err := s.Updater.Tick(ctx, settings); err != nil {
			s.log().Warn("vulnscan: addon image updates", "err", err)
		}
	}
}

// sweepDue: the first tick at/after hourUTC with no sweep in 20h.
func sweepDue(now time.Time, hourUTC int, last time.Time) bool {
	if now.UTC().Hour() < hourUTC {
		return false
	}
	return last.IsZero() || now.Sub(last) >= sweepEvery
}

func (s *Sweeper) lastSweep(ctx context.Context) (time.Time, error) {
	raw, err := s.DB.GetSetting(ctx, lastSweepKey)
	if err != nil || raw == "" {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, nil // a garbled marker just means "sweep now"
	}
	return t, nil
}

// Sweep refreshes the inventory and queues a nightly scan for every
// live digest an enabled owner runs and no scan in the last 20h covers.
// Returns how many scans it queued. Also applies retention.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	settings, err := s.DB.GetVulnScanSettings(ctx)
	if err != nil {
		return 0, err
	}
	if settings.Mode == ModeOff {
		return 0, nil
	}
	now := s.now()
	last, err := s.lastSweep(ctx)
	if err != nil {
		return 0, err
	}
	// The first sweep ever (or a retry of one that died before recording
	// itself) baselines. Marked before anything is queued so a scan that
	// fails right away is already covered.
	first := last.IsZero()
	if first {
		if err := s.DB.SetSetting(ctx, baselineKey, baselinePending, "vulnscan"); err != nil {
			return 0, err
		}
	}
	inv, err := Inventory(ctx, s.Cluster)
	if err != nil {
		return 0, err
	}
	if err := s.DB.UpsertVulnImages(ctx, inv, now); err != nil {
		return 0, err
	}
	policies, err := s.DB.ListVulnPolicies(ctx)
	if err != nil {
		return 0, err
	}
	want := map[string]db.VulnImage{}
	var digests []string
	for _, im := range inv {
		if !ownerEnabled(settings, policies, im) {
			continue
		}
		if _, dup := want[im.Digest]; !dup {
			want[im.Digest] = im
			digests = append(digests, im.Digest)
		}
	}
	latest, err := s.DB.LatestSucceededVulnScans(ctx, digests)
	if err != nil {
		return 0, err
	}
	sort.Strings(digests)
	queued := 0
	var maxID int64
	for _, d := range digests {
		if sc, ok := latest[d]; ok && now.Sub(sc.FinishedAt) < ReuseWindow {
			continue
		}
		im := want[d]
		id, created, err := s.DB.EnqueueVulnScan(ctx, db.VulnScanRequest{
			Digest: d, ImageRef: im.ImageRef, Reason: db.VulnReasonNightly, PullSecret: im.PullSecret,
		})
		if err != nil {
			return queued, err
		}
		maxID = max(maxID, id)
		if created {
			queued++
		}
	}
	if first {
		if err := s.DB.SetSetting(ctx, baselineKey, strconv.FormatInt(maxID, 10), "vulnscan"); err != nil {
			return queued, err
		}
	}
	if err := s.DB.SetSetting(ctx, lastSweepKey, now.UTC().Format(time.RFC3339), "vulnscan"); err != nil {
		return queued, err
	}
	if n, err := s.DB.PruneVulnScans(ctx, now, retainPerImage, pruneGoneAfter); err != nil {
		s.log().Warn("vulnscan: prune", "err", err)
	} else if n > 0 {
		s.log().Info("vulnscan: pruned old scans", "deleted", n)
	}
	if imgs, notified, err := s.DB.PruneVulnInventory(ctx, now, pruneGoneAfter); err != nil {
		s.log().Warn("vulnscan: prune inventory", "err", err)
	} else if imgs+notified > 0 {
		s.log().Info("vulnscan: pruned gone inventory", "images", imgs, "notified", notified)
	}
	return queued, nil
}

func ownerEnabled(st db.VulnScanSettings, policies map[string]string, im db.VulnImage) bool {
	switch im.OwnerKind {
	case db.VulnOwnerService:
		return EffectiveMode(st.Mode, policies[im.Project+"/"+im.Service]) != ModeOff
	case db.VulnOwnerAddon:
		return st.Mode != ModeOff && st.ScanAddons
	case db.VulnOwnerPlatform:
		return st.Mode != ModeOff && st.ScanPlatform
	}
	return false
}

// OwnerKey identifies an owner in VulnNotified. db.PruneVulnInventory
// rebuilds it in SQL; keep the two in step.
func OwnerKey(im db.VulnImage) string {
	switch im.OwnerKind {
	case db.VulnOwnerService:
		return "service:" + im.Project + "/" + im.Service
	case db.VulnOwnerAddon:
		return "addon:" + im.Project + "/" + im.Addon
	}
	return "platform:" + im.Namespace + "/" + im.Workload
}

// IgnoreScope returns the VulnIgnore (scope, project, service) an
// owner's ignores are stored under.
func IgnoreScope(im db.VulnImage) (scope, project, name string) {
	switch im.OwnerKind {
	case db.VulnOwnerService:
		return db.VulnOwnerService, im.Project, im.Service
	case db.VulnOwnerAddon:
		return db.VulnOwnerAddon, im.Project, im.Addon
	}
	return db.VulnOwnerPlatform, "", ""
}

func notifyOwner(im db.VulnImage) notify.VulnOwner {
	return notify.VulnOwner{Kind: im.OwnerKind, Project: im.Project, Service: im.Service, Addon: im.Addon,
		Namespace: im.Namespace, Workload: im.Workload}
}

// baseline reads the first-sweep marker: active while the first sweep
// is queueing or its scans run; through is the highest scan id it
// covers (0 while still queueing: every scan is covered).
func (s *Sweeper) baseline(ctx context.Context) (active bool, through int64, err error) {
	raw, err := s.DB.GetSetting(ctx, baselineKey)
	if err != nil {
		return false, 0, err
	}
	switch raw {
	case "", baselineDone:
		return false, 0, nil
	case baselinePending:
		return true, 0, nil
	}
	n, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil {
		return false, 0, nil // garbled: alert normally rather than go silent forever
	}
	return true, n, nil
}

// AlertNew emits one vulnerabilities_found per owner whose latest
// successful scans hold fixable critical/high findings not yet notified
// for it. Owners with a scan still queued/running wait, so a sweep's
// results arrive as one alert, not one per image. While the first-ever
// sweep runs nothing alerts; when it's done finishBaseline records every
// finding as notified and sends one summary.
func (s *Sweeper) AlertNew(ctx context.Context, st db.VulnScanSettings) error {
	if st.Mode == ModeOff {
		return nil
	}
	active, through, err := s.baseline(ctx)
	if err != nil {
		return err
	}
	if active {
		if through == 0 {
			return nil // first sweep still queueing (or died doing so)
		}
		n, err := s.DB.CountActiveVulnScansThrough(ctx, through)
		if err != nil || n > 0 {
			return err
		}
		return s.finishBaseline(ctx, st)
	}
	if s.Notify == nil || st.NotifyMinSeverity == "none" {
		return nil
	}
	now := s.now()
	inv, err := s.DB.ListVulnImages(ctx, db.VulnImageFilter{SeenSince: now.Add(-GoneAfter)})
	if err != nil {
		return err
	}
	policies, err := s.DB.ListVulnPolicies(ctx)
	if err != nil {
		return err
	}
	type owner struct {
		img     db.VulnImage
		digests []string
	}
	owners := map[string]*owner{}
	var keys, all []string
	for _, im := range inv {
		if !ownerEnabled(st, policies, im) {
			continue
		}
		k := OwnerKey(im)
		o, ok := owners[k]
		if !ok {
			o = &owner{img: im}
			owners[k] = o
			keys = append(keys, k)
		}
		o.digests = append(o.digests, im.Digest)
		all = append(all, im.Digest)
	}
	latest, err := s.DB.LatestSucceededVulnScans(ctx, all)
	if err != nil {
		return err
	}
	sort.Strings(keys)
	since := s.alertedThrough
	pending := false
	findingsByScan := map[int64][]db.VulnFinding{}
	for _, k := range keys {
		o := owners[k]
		fresh := false
		for _, d := range o.digests {
			if sc, ok := latest[d]; ok && sc.FinishedAt.After(since) {
				fresh = true
			}
		}
		if !fresh {
			continue
		}
		active, err := s.DB.CountActiveVulnScans(ctx, o.digests)
		if err != nil {
			return err
		}
		if active > 0 {
			pending = true
			continue
		}
		var findings []db.VulnFinding
		for _, d := range o.digests {
			sc, ok := latest[d]
			if !ok {
				continue
			}
			fs, cached := findingsByScan[sc.ID]
			if !cached {
				if fs, err = s.DB.ListVulnFindings(ctx, sc.ID); err != nil {
					return err
				}
				findingsByScan[sc.ID] = fs
			}
			findings = append(findings, fs...)
		}
		scope, project, name := IgnoreScope(o.img)
		ignores, err := s.DB.ListVulnIgnores(ctx, scope, project, name)
		if err != nil {
			return err
		}
		ids, err := s.DB.UnnotifiedVulnIDs(ctx, k, AlertCandidates(findings, ignores, st.NotifyMinSeverity, now))
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			continue
		}
		if err := s.Notify.EmitDurable(notify.VulnerabilitiesFound(notifyOwner(o.img), alertItems(findings, ids))); err != nil {
			s.log().Warn("vulnscan: emit vulnerabilities_found", "owner", k, "err", err)
			pending = true // retried next tick; nothing was recorded
			continue
		}
		if err := s.DB.MarkVulnsNotified(ctx, k, ids); err != nil {
			return err
		}
	}
	if !pending {
		s.alertedThrough = now
	}
	return nil
}

// finishBaseline marks every current alert candidate of every enabled
// owner notified, emits one summary, and ends the baseline.
func (s *Sweeper) finishBaseline(ctx context.Context, st db.VulnScanSettings) error {
	now := s.now()
	inv, err := s.DB.ListVulnImages(ctx, db.VulnImageFilter{SeenSince: now.Add(-GoneAfter)})
	if err != nil {
		return err
	}
	policies, err := s.DB.ListVulnPolicies(ctx)
	if err != nil {
		return err
	}
	byOwner := map[string][]string{}
	imgOf := map[string]db.VulnImage{}
	var all []string
	for _, im := range inv {
		if !ownerEnabled(st, policies, im) {
			continue
		}
		k := OwnerKey(im)
		byOwner[k] = append(byOwner[k], im.Digest)
		imgOf[k] = im
		all = append(all, im.Digest)
	}
	latest, err := s.DB.LatestSucceededVulnScans(ctx, all)
	if err != nil {
		return err
	}
	findingsByScan := map[int64][]db.VulnFinding{}
	for k, digests := range byOwner {
		var findings []db.VulnFinding
		for _, d := range digests {
			sc, ok := latest[d]
			if !ok {
				continue
			}
			fs, cached := findingsByScan[sc.ID]
			if !cached {
				if fs, err = s.DB.ListVulnFindings(ctx, sc.ID); err != nil {
					return err
				}
				findingsByScan[sc.ID] = fs
			}
			findings = append(findings, fs...)
		}
		scope, project, name := IgnoreScope(imgOf[k])
		ignores, err := s.DB.ListVulnIgnores(ctx, scope, project, name)
		if err != nil {
			return err
		}
		// "high" covers everything any notifyMinSeverity would alert on,
		// so lowering the threshold later doesn't replay the backlog.
		if err := s.DB.MarkVulnsNotified(ctx, k, AlertCandidates(findings, ignores, "high", now)); err != nil {
			return err
		}
	}
	withCrit := 0
	for _, sc := range latest {
		if sc.Counts.FixableCritical > 0 {
			withCrit++
		}
	}
	if s.Notify != nil && st.NotifyMinSeverity != "none" && len(latest) > 0 {
		if err := s.Notify.EmitDurable(notify.VulnBaselineComplete(len(latest), withCrit)); err != nil {
			s.log().Warn("vulnscan: emit baseline summary", "err", err)
		}
	}
	if err := s.DB.SetSetting(ctx, baselineKey, baselineDone, "vulnscan"); err != nil {
		return fmt.Errorf("record baseline done: %w", err)
	}
	s.alertedThrough = now
	s.log().Info("vulnscan: baseline scan complete", "images", len(latest), "withFixableCritical", withCrit)
	return nil
}

// alertItems lists ids (criticals first) with one finding's details each.
func alertItems(findings []db.VulnFinding, ids []string) []notify.VulnItem {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []notify.VulnItem
	for _, f := range findings {
		if want[f.VulnID] {
			delete(want, f.VulnID)
			out = append(out, notify.VulnItem{VulnID: f.VulnID, Severity: f.Severity, PkgName: f.PkgName,
				InstalledVersion: f.InstalledVersion, FixedVersion: f.FixedVersion})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Severity == "CRITICAL" && out[j].Severity != "CRITICAL"
	})
	return out
}

// OnScanFailed alerts on a failed nightly/manual scan (gate failures are
// reported by the build). Only the first failure in a row alerts, so an
// image that can't be pulled doesn't page every night.
func (s *Sweeper) OnScanFailed(ctx context.Context, scan db.VulnScan, msg string) {
	if s.Notify == nil || scan.Reason == db.VulnReasonGate {
		return
	}
	if scan.Reason == db.VulnReasonNightly {
		if active, through, err := s.baseline(ctx); err == nil && active && (through == 0 || scan.ID <= through) {
			s.log().Info("vulnscan: baseline scan failed (not alerted)", "scan", scan.ID, "image", scan.ImageRef, "err", msg)
			return
		}
	}
	owner := db.VulnImage{OwnerKind: db.VulnOwnerPlatform, Workload: scan.ImageRef}
	if scan.Digest != "" {
		if prev, err := s.DB.PreviousFinishedVulnScan(ctx, scan.Digest, scan.ID); err == nil && prev != nil && prev.Status == db.VulnStatusFailed {
			return
		}
		if inv, err := s.DB.ListVulnImages(ctx, db.VulnImageFilter{Digest: scan.Digest}); err == nil && len(inv) > 0 {
			owner = inv[0]
		}
	}
	if err := s.Notify.EmitDurable(notify.VulnScanFailed(notifyOwner(owner), scan.ImageRef, msg)); err != nil {
		s.log().Warn("vulnscan: emit scan_failed", "scan", scan.ID, "err", err)
	}
}
