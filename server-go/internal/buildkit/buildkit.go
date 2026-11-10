// Package buildkit holds the shared-daemon topology the build pipeline
// runs against: how many buildkitd daemons exist, which one a project's
// builds go to, and how many builds the daemons can usefully absorb.
//
// Default topology is the single kuso-buildkitd Deployment
// (deploy/buildkitd.yaml). Setting KUSO_BUILDKITD_SHARDS=N (N >= 1) on
// kuso-server routes builds to the opt-in StatefulSet in
// deploy/optional/buildkitd-shards.yaml instead, one shard per project
// hash so each project keeps its layer cache on one daemon.
package buildkit

import (
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
)

const (
	// DefaultHost is the single shared daemon's Service.
	DefaultHost = "tcp://kuso-buildkitd.kuso.svc.cluster.local:1234"

	// shardHostFmt addresses one StatefulSet pod through its headless
	// Service.
	shardHostFmt = "tcp://kuso-buildkitd-shard-%d.kuso-buildkitd-shard.kuso.svc.cluster.local:1234"

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

// Shards returns KUSO_BUILDKITD_SHARDS, or 0 (single Deployment) when
// unset or invalid.
func Shards() int {
	n, err := strconv.Atoi(os.Getenv("KUSO_BUILDKITD_SHARDS"))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// Daemons is the number of buildkitd processes builds spread over.
func Daemons() int {
	if n := Shards(); n > 0 {
		return n
	}
	return 1
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

// HostFor returns the buildkitd address a project's builds use.
func HostFor(project string) string {
	return hostFor(project, Shards())
}

func hostFor(project string, shards int) string {
	if shards < 1 {
		return DefaultHost
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(project))
	return fmt.Sprintf(shardHostFmt, h.Sum32()%uint32(shards))
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
