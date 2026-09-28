// GitHub commit statuses for builds.
//
// Rather than calling out from every transition site (markSucceeded,
// markFailed, cancelBuild, stampHeldSuperseded, stampHoldExpired,
// markReleaseFailed, buildcontroller.giveUp, …) the poller reconciles:
// each tick it derives the status a build SHOULD have from its phase
// annotations and posts only when that differs from what was last
// posted (annCommitStatus). New terminal paths are covered for free, a
// post that fails is retried, and a server restart mid-transition
// resumes where it left off.
//
// The builds package doesn't import github: CommitStatusReporter is
// implemented by an adapter in cmd/kuso-server (same pattern as
// EventEmitter).
package builds

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/failures"
	"kuso/server/internal/kube"
)

const (
	// annCommitStatus is the key (see desiredCommitStatus) of the last
	// status successfully posted to GitHub for this build.
	annCommitStatus = "kuso.sislelabs.com/commit-status"
	// annCommitStatusContext pins the status context chosen on the first
	// post, so later transitions update the same GitHub status row even
	// if the service's envs change mid-build.
	annCommitStatusContext = "kuso.sislelabs.com/commit-status-context"

	// CommitStatusContextPrefix prefixes every context kuso posts. The CI
	// gate ignores statuses under it so kuso never waits on itself.
	CommitStatusContextPrefix = "kuso/"

	commitStatusDescMax = 140
	// A build with no posted status that finished longer ago than this is
	// history, not a transition — don't backfill it on first rollout.
	commitStatusBackfillWindow = time.Hour
	commitStatusRetryMin       = 30 * time.Second
	commitStatusRetryMax       = 10 * time.Minute
)

// CommitStatus is one GitHub commit status to post. TargetPath is the
// dashboard path; the adapter makes it absolute.
type CommitStatus struct {
	InstallationID int64
	Owner          string
	Repo           string
	SHA            string
	Context        string
	State          string // pending | success | failure | error
	Description    string
	TargetPath     string
}

// CommitStatusReporter posts commit statuses. Errors are logged and
// retried with backoff; they never affect the build.
type CommitStatusReporter interface {
	PostCommitStatus(ctx context.Context, s CommitStatus) error
}

type statusRetryState struct {
	next    time.Time
	backoff time.Duration
}

// githubCommitTarget reports whether b can carry a commit status: a real
// SHA on a github.com repo the App is installed on.
func githubCommitTarget(b *kube.KusoBuild) (installationID int64, owner, repo, sha string, ok bool) {
	if b == nil || b.Spec.Repo == nil || b.Spec.GithubInstallationID <= 0 || !shaRE.MatchString(b.Spec.Ref) {
		return 0, "", "", "", false
	}
	owner, repo = splitGithubURL(b.Spec.Repo.URL)
	if owner == "" || repo == "" {
		return 0, "", "", "", false
	}
	return b.Spec.GithubInstallationID, owner, repo, b.Spec.Ref, true
}

// desiredCommitStatus maps a build's phase annotations to the GitHub
// status it should show. key identifies the state for change detection
// (description-only changes don't re-post).
func desiredCommitStatus(b *kube.KusoBuild) (key, state, desc string) {
	a := b.Annotations
	msg := a[annMessage]
	switch buildPhase(b) {
	case "succeeded":
		if b.Spec.DryRun {
			return "success", "success", "Build succeeded (dry run, not deployed)"
		}
		return "success", "success", "Deployed"
	case "failed":
		return "failure", "failure", statusDesc("Build failed", failureSummary(a, msg))
	case "release-failed":
		return "failure", "failure", statusDesc("Release hook failed", failureSummary(a, lastLine(msg)))
	case "cancelled":
		switch a[annCIGate] {
		case ciGateFailed, ciGateTimedOut:
			return "failure", "failure", statusDesc("", msg)
		}
		if a[annSupersededBy] != "" {
			return "error", "error", "Superseded by a newer build"
		}
		return "error", "error", statusDesc("Cancelled", msg)
	case "queued":
		if ciGateHolds(b) {
			return "pending:ci", "pending", "Waiting for CI checks to pass"
		}
		return "pending:queued", "pending", "Queued"
	}
	if a[annPromoteHold] != "" {
		return "pending:held", "pending", "Built; waiting for sibling builds before deploying"
	}
	return "pending:building", "pending", "Building"
}

func failureSummary(a map[string]string, fallback string) string {
	if raw := a[annClassification]; raw != "" {
		var c failures.Classification
		if json.Unmarshal([]byte(raw), &c) == nil && strings.TrimSpace(c.Summary) != "" {
			return c.Summary
		}
	}
	return fallback
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// statusDesc joins prefix + detail onto one line and caps it at GitHub's
// 140-character description limit.
func statusDesc(prefix, detail string) string {
	detail = strings.Join(strings.Fields(detail), " ")
	s := prefix
	switch {
	case prefix == "":
		s = detail
	case detail != "":
		s = prefix + ": " + detail
	}
	if utf8.RuneCountInString(s) <= commitStatusDescMax {
		return s
	}
	r := []rune(s)
	return string(r[:commitStatusDescMax-1]) + "…"
}

// commitStatusContext returns the context for b plus the env it names
// ("" = production or ambiguous).
func (p *Poller) commitStatusContext(ctx context.Context, ns string, b *kube.KusoBuild) (string, string) {
	short := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
	if pinned := b.Annotations[annCommitStatusContext]; pinned != "" {
		env := ""
		if i := strings.LastIndex(pinned, " ("); i > 0 && strings.HasSuffix(pinned, ")") {
			env = pinned[i+2 : len(pinned)-1]
		}
		return pinned, env
	}
	var kc *kube.Client
	var homeNS string
	if p.Svc != nil {
		kc, homeNS = p.Svc.Kube, p.Svc.Namespace
	}
	env := singleTargetEnv(lookupBuildTargets(ctx, kc, ns, homeNS, b))
	if env == "" || env == "production" {
		return CommitStatusContextPrefix + short, ""
	}
	return fmt.Sprintf("%s%s (%s)", CommitStatusContextPrefix, short, env), env
}

// syncCommitStatuses posts a status for every build in builds whose
// desired status differs from the last one posted. Posting runs off the
// tick goroutine (one in flight per build) so a slow GitHub can't stall
// the poller heartbeat.
func (p *Poller) syncCommitStatuses(ctx context.Context, ns string, builds []kube.KusoBuild) {
	if p.CommitStatuses == nil {
		return
	}
	now := time.Now()
	p.statusMu.Lock()
	if p.statusPosted == nil {
		p.statusPosted = map[string]string{}
		p.statusRetry = map[string]statusRetryState{}
	}
	p.statusMu.Unlock()
	for i := range builds {
		b := &builds[i]
		inst, owner, repo, sha, ok := githubCommitTarget(b)
		if !ok {
			continue
		}
		key, state, desc := desiredCommitStatus(b)
		id := ns + "/" + b.Name
		posted := b.Annotations[annCommitStatus]
		p.statusMu.Lock()
		if mem, ok := p.statusPosted[id]; ok {
			if mem == posted {
				// The informer caught up with our stamp; the annotation is
				// authoritative again.
				delete(p.statusPosted, id)
			} else {
				posted = mem
			}
		}
		retry, backingOff := p.statusRetry[id]
		p.statusMu.Unlock()
		if posted == key {
			continue
		}
		if backingOff && now.Before(retry.next) {
			continue
		}
		if posted == "" && b.Labels[LabelBuildState] == BuildStateDone && finishedBefore(b, now.Add(-commitStatusBackfillWindow)) {
			continue
		}
		build := copyBuild(b)
		p.goDetached(ctx, "status/"+id, func(ctx context.Context) {
			sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			statusCtx, env := p.commitStatusContext(sctx, ns, build)
			short := strings.TrimPrefix(build.Spec.Service, build.Spec.Project+"-")
			path := buildEventURL(build.Spec.Project, short) + "&tab=deployments"
			if env != "" {
				path = withEnvParam(path, []buildTarget{{Env: env}})
			}
			err := p.CommitStatuses.PostCommitStatus(sctx, CommitStatus{
				InstallationID: inst, Owner: owner, Repo: repo, SHA: sha,
				Context: statusCtx, State: state, Description: desc, TargetPath: path,
			})
			p.statusMu.Lock()
			if err != nil {
				r := p.statusRetry[id]
				r.backoff = min(max(r.backoff*2, commitStatusRetryMin), commitStatusRetryMax)
				r.next = time.Now().Add(r.backoff)
				p.statusRetry[id] = r
				p.statusMu.Unlock()
				p.logger().Warn("commit status post failed; will retry",
					"build", build.Name, "context", statusCtx, "state", state, "retryIn", r.backoff, "err", err)
				return
			}
			delete(p.statusRetry, id)
			p.statusPosted[id] = key
			p.statusMu.Unlock()
			patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q,%q:%q}}}`,
				annCommitStatus, key, annCommitStatusContext, statusCtx)
			if _, perr := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
				Patch(sctx, build.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
				// The in-memory statusPosted entry still dedupes until restart.
				p.logger().Warn("stamp commit-status annotation", "build", build.Name, "err", perr)
			}
		})
	}
}

// copyBuild detaches b from the caller's list (the next tick's List
// reuses the backing array) for a detached goroutine that may mutate
// its annotation map.
func copyBuild(b *kube.KusoBuild) *kube.KusoBuild {
	c := *b
	c.Annotations = maps.Clone(b.Annotations)
	c.Labels = maps.Clone(b.Labels)
	return &c
}

// finishedBefore reports whether b completed before cutoff. Builds with no
// parseable completion time fall back to their creation time.
func finishedBefore(b *kube.KusoBuild, cutoff time.Time) bool {
	if t, err := time.Parse(time.RFC3339, b.Annotations[annCompletedAt]); err == nil {
		return t.Before(cutoff)
	}
	return !b.CreationTimestamp.IsZero() && b.CreationTimestamp.Time.Before(cutoff)
}

// detachedRunner runs per-build side work (status posts, CI gate checks)
// off the tick goroutine: at most one job per key, bounded concurrency.
type detachedRunner struct {
	once sync.Once
	sem  chan struct{}
	mu   sync.Mutex
	busy map[string]struct{}
	wg   sync.WaitGroup
}

const detachedConcurrency = 4

func (p *Poller) goDetached(ctx context.Context, key string, fn func(context.Context)) {
	d := &p.detached
	d.once.Do(func() {
		d.sem = make(chan struct{}, detachedConcurrency)
		d.busy = map[string]struct{}{}
	})
	d.mu.Lock()
	if _, ok := d.busy[key]; ok {
		d.mu.Unlock()
		return
	}
	d.busy[key] = struct{}{}
	d.mu.Unlock()
	d.wg.Add(1)
	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.busy, key)
			d.mu.Unlock()
			d.wg.Done()
		}()
		select {
		case d.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-d.sem }()
		fn(ctx)
	}()
}

// waitDetached blocks until every detached job has finished. Tests use it
// to observe the outcome of a sync pass.
func (p *Poller) waitDetached() { p.detached.wg.Wait() }
