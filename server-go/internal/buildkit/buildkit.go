// Package buildkit holds the build-daemon topology the build pipeline
// runs against: which daemons exist, which one a project's builds go
// to, and how many builds the daemons can usefully absorb.
//
// deploy/buildkitd.yaml runs one kuso-buildkitd per eligible node (a
// DaemonSet: every non-control-plane node unless labelled
// kuso.sislelabs.com/builds=off). Each project's builds stick to one
// daemon, chosen by rendezvous hash over the Ready daemons' node names,
// so its layer cache stays warm and a node joining or leaving only moves
// the projects that hash to that node.
package buildkit

import (
	"fmt"
	"hash/fnv"
	"os"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
)

const (
	// DefaultHost is the daemons' ClusterIP Service: the fallback when
	// no Ready daemon pod is known (pod cache not synced yet). It
	// load-balances, so a build sent there is correct but may be
	// cache-cold.
	DefaultHost = "tcp://kuso-buildkitd.kuso.svc.cluster.local:1234"

	// PodLabel selects the daemon pods.
	PodLabel = "app.kubernetes.io/name=kuso-buildkitd"

	port = 1234

	// DefaultMaxParallelism mirrors max-parallelism in the
	// kuso-buildkitd-config ConfigMap.
	DefaultMaxParallelism = 3

	// buildsPerSlot is how many admitted builds share one vertex slot.
	// A build spends much of its life outside RUN steps (clone, context
	// upload, cache import, push), so 2 per slot keeps the daemon busy
	// without queueing inside it.
	buildsPerSlot = 2

	minCap = 2
)

// Daemon is one Ready build daemon.
type Daemon struct {
	Node string
	IP   string
}

// Host is the daemon's buildctl address. Build Jobs verify the daemon
// certificate against --tlsservername, so dialling the pod IP is fine.
func (d Daemon) Host() string { return fmt.Sprintf("tcp://%s:%d", d.IP, port) }

// ReadyDaemons returns the daemon pods that can take a build now:
// Running, Ready, with an IP, not terminating. One per node; if a node
// briefly has two (a pod being replaced), the first wins.
func ReadyDaemons(pods []*corev1.Pod) []Daemon {
	seen := map[string]bool{}
	var out []Daemon
	for _, p := range pods {
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning || p.Status.PodIP == "" || p.Spec.NodeName == "" {
			continue
		}
		if !podReady(p) || seen[p.Spec.NodeName] {
			continue
		}
		seen[p.Spec.NodeName] = true
		out = append(out, Daemon{Node: p.Spec.NodeName, IP: p.Status.PodIP})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// Pick returns the daemon a project's builds use: highest rendezvous
// score over (project, node). Keyed by node, not pod, so a daemon
// restart (new pod IP) keeps the project on the same cache.
func Pick(project string, daemons []Daemon) (Daemon, bool) {
	var best Daemon
	var bestScore uint64
	found := false
	for _, d := range daemons {
		h := fnv.New64a()
		_, _ = h.Write([]byte(project))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(d.Node))
		if s := h.Sum64(); !found || s > bestScore {
			best, bestScore, found = d, s, true
		}
	}
	return best, found
}

// HostFor returns the address a project's next build uses.
func HostFor(project string, daemons []Daemon) string {
	if d, ok := Pick(project, daemons); ok {
		return d.Host()
	}
	return DefaultHost
}

// MaxParallelism returns KUSO_BUILDKITD_MAX_PARALLELISM, else the
// ConfigMap default. Keep the two in sync when tuning the daemon.
func MaxParallelism() int {
	n, err := strconv.Atoi(os.Getenv("KUSO_BUILDKITD_MAX_PARALLELISM"))
	if err != nil || n < 1 {
		return DefaultMaxParallelism
	}
	return n
}

// DefaultBuildCap is the automatic cluster-wide concurrent-build cap.
//
// Every build is a thin buildctl client; the compile/RUN/push work runs
// inside the daemons, so the ceiling is daemon capacity
// (daemons × max-parallelism × buildsPerSlot), not cluster CPU. Cluster
// CPU only bounds it from below on small boxes (allocatable / 4 cores),
// and the result never drops under 2. allocMilliCPU <= 0 means unknown.
func DefaultBuildCap(allocMilliCPU int64, daemons, parallelism int) int {
	if daemons < 1 {
		daemons = 1
	}
	if parallelism < 1 {
		parallelism = DefaultMaxParallelism
	}
	c := daemons * parallelism * buildsPerSlot
	if allocMilliCPU > 0 {
		if byCPU := int(allocMilliCPU / 4000); byCPU < c {
			c = byCPU
		}
	}
	return max(c, minCap)
}
