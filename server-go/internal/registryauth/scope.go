package registryauth

import (
	"fmt"
	"strings"
)

// inClusterRegistryName is the Service name every alias of the in-cluster
// registry starts with (kuso-registry, kuso-registry.kuso,
// kuso-registry.kuso.svc.cluster.local, with or without :5000).
const inClusterRegistryName = "kuso-registry"

// ForeignRepo reports whether repository (an image ref without tag, or a
// full ref) points into the in-cluster registry under a project other
// than project. Kubelets pull from the in-cluster registry with the node
// credential, which can read every repository, so a runtime=image service
// or a command cron naming "<registry>/<other-project>/<svc>" would run
// another project's image (review SEC-2). Refs outside the in-cluster
// registry are never foreign.
func ForeignRepo(project, repository string) bool {
	ref := strings.ToLower(strings.TrimSpace(repository))
	if i := strings.Index(ref, "://"); i >= 0 {
		ref = ref[i+3:]
	}
	host, path, ok := strings.Cut(ref, "/")
	if !ok {
		return false
	}
	hostname, _, _ := strings.Cut(host, ":")
	if hostname != inClusterRegistryName && !strings.HasPrefix(hostname, inClusterRegistryName+".") {
		return false
	}
	owner, _, _ := strings.Cut(path, "/")
	return owner != strings.ToLower(project)
}

// ForeignRepoReason is the user-facing rejection for a foreign repository;
// callers wrap it in their own package's invalid-input error.
func ForeignRepoReason(field, project string) string {
	return fmt.Sprintf("%s may only name this project's images on the in-cluster registry (%s.../%s/<service>)", field, inClusterRegistryName, project)
}
