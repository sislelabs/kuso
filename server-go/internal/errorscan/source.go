package errorscan

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"kuso/server/internal/db"
)

// Only a service's runtime output counts as its errors. Build pods, release
// (migration) hooks, preview seed/migrate Jobs, addon backups and the
// updater also carry the project/service labels, so logship stores their
// lines under the service too; a failed `npm ci` or a migration's "ERROR:"
// is reported on the Deployments tab, and counting it again as a runtime
// error made the canvas badge disagree with what the service was doing.
//
// LogLine has no column for the pod's kind (adding one means a migration
// on the partitioned table), so the scanner learns it two ways:
//   - PodKinds, filled by logship from the pod labels it already lists.
//     Exact, but only when both singletons run in this process.
//   - nonRuntimeHeuristic, from the pod name and line shape alone. Covers a
//     scanner on another replica, a backlog after a restart, and the rows
//     stored before this filter existed (the db readers hide those).

// Pod label values that mark non-runtime workloads.
const (
	labelRole      = "kuso.sislelabs.com/role"
	labelComponent = "app.kubernetes.io/component"
	labelName      = "app.kubernetes.io/name"
)

// RuntimeWorkload reports whether a pod with these labels runs the service
// itself (an env Deployment, a cron or a run) rather than a build, a
// release hook or platform housekeeping. Unknown kinds count as runtime so
// a new workload type is never silently dropped from the error feed.
func RuntimeWorkload(labels map[string]string) bool {
	if labels[labelRole] != "" {
		// release, preview-seed, preview-migrate, backup, updater, …
		return false
	}
	if labels[labelComponent] == "kusobuild" || labels[labelName] == "kusobuild" {
		return false
	}
	return true
}

// The read-side SQL (db.ErrorEvent readers) applies the same patterns.
var (
	buildkitStepRe = regexp.MustCompile(db.BuildkitStepPattern)
	releasePodRe   = regexp.MustCompile(db.ReleasePodPattern)
)

// nonRuntimeHeuristic classifies a line without the pod's labels.
func nonRuntimeHeuristic(pod, line string) bool {
	return buildkitStepRe.MatchString(line) || releasePodRe.MatchString(pod)
}

// PodKinds remembers which pods logship saw are non-runtime, keyed by pod
// name. Entries expire podKindTTL after the last sighting so the map stays
// bounded by the pods that existed recently.
type PodKinds struct {
	mu   sync.Mutex
	seen map[string]podKind
	now  func() time.Time
}

type podKind struct {
	runtime bool
	at      time.Time
}

// podKindTTL outlives the scanner's lag (seconds) by a wide margin; a pod
// gone longer than this has had its lines scanned long ago.
const podKindTTL = 2 * time.Hour

// NewPodKinds returns an empty registry.
func NewPodKinds() *PodKinds {
	return &PodKinds{seen: map[string]podKind{}, now: time.Now}
}

// Observe records a pod's kind from its labels.
func (k *PodKinds) Observe(pod string, labels map[string]string) {
	if k == nil || pod == "" {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	k.seen[pod] = podKind{runtime: RuntimeWorkload(labels), at: now}
	if len(k.seen)%256 == 0 {
		k.sweepLocked(now)
	}
}

// Lookup returns the pod's kind and whether it is known.
func (k *PodKinds) Lookup(pod string) (runtime, known bool) {
	if k == nil {
		return true, false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	pk, ok := k.seen[pod]
	if !ok || k.now().Sub(pk.at) > podKindTTL {
		return true, false
	}
	return pk.runtime, true
}

func (k *PodKinds) sweepLocked(now time.Time) {
	for name, pk := range k.seen {
		if now.Sub(pk.at) > podKindTTL {
			delete(k.seen, name)
		}
	}
}

// Sweep drops expired entries. logship calls it once per reconcile.
func (k *PodKinds) Sweep() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sweepLocked(k.now())
}

// runtimeLine decides whether a stored log line can be a runtime error.
func (s *Scanner) runtimeLine(pod, line string) bool {
	if rt, known := s.Pods.Lookup(pod); known {
		return rt
	}
	return !nonRuntimeHeuristic(pod, strings.TrimSpace(line))
}
