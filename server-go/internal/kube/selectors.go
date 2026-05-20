package kube

import (
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
)

// LabelPrefix is the user-visible kuso label namespace. Workload
// labels look like kuso.sislelabs.com/<key>; the labels editor
// strips/re-applies it so the user types `role` and the kube label
// becomes `kuso.sislelabs.com/role`.
const LabelPrefix = "kuso.sislelabs.com/"

// Standard label keys used across the codebase. Promoted here so the
// kube/projects/addons/builds/secrets packages all reach for one set
// of constants instead of re-typing the strings.
const (
	LabelProject = LabelPrefix + "project"
	LabelService = LabelPrefix + "service"
	LabelEnv     = LabelPrefix + "env"
)

// LabelSelector builds a properly-formatted kube label selector
// string from pairs, escaping anything that could be interpreted as
// selector syntax. Going through labels.SelectorFromSet (which
// validates each value via labels.IsValidLabelValue under the hood)
// prevents the class of bug where a project name like "foo," would
// be appended into a selector string via string concatenation and
// re-shape the query at the apiserver.
//
// Pairs with empty values are dropped. An empty map returns "" —
// meaning "no selector" to ListOptions, which selects everything.
// Callers that need "everything" should pass an empty map
// deliberately; the more common case is to error out before reaching
// this function if any value is empty.
func LabelSelector(pairs map[string]string) string {
	clean := make(labels.Set, len(pairs))
	for k, v := range pairs {
		if v == "" {
			continue
		}
		clean[k] = v
	}
	return labels.SelectorFromSet(clean).String()
}

// SharedSecretNames returns the two always-present shared-secret
// entries every KusoEnvironment's spec.envFromSecrets must carry: the
// project-shared secret (<project>-shared) and the instance-shared
// secret (kuso-instance-shared). Both are marked optional:true by the
// kusoenvironment Helm chart, so a pod boots cleanly even when the
// Secret has not been created yet.
//
// Single source of truth: addons.RefreshEnvSecrets and the two env-CR
// creation paths in the projects package all build envFromSecrets by
// appending this — so the three sites cannot drift and silently drop
// shared secrets (which is exactly the bug this helper fixes).
func SharedSecretNames(project string) []string {
	return []string{project + "-shared", "kuso-instance-shared"}
}

// envSecretNameRE strips characters that aren't valid in a Kubernetes
// resource-name segment, so an env name can be interpolated into a
// Secret name safely. Matches secrets.Name's historical sanitization.
var envSecretNameRE = regexp.MustCompile(`[^a-z0-9-]`)

// ServiceSecretName returns the service-scoped shared secret name:
// <project>-<service>-secrets. This Secret holds keys set via
// `kuso secret set <project> <service> KEY VALUE` with no --env scope.
// Like the project-shared secret it is marked optional:true by the
// kusoenvironment Helm chart, so referencing it before it exists is safe.
func ServiceSecretName(project, service string) string {
	return project + "-" + service + "-secrets"
}

// EnvSecretName returns the env-scoped secret name:
// <project>-<service>-<sanitized-env>-secrets. The env name is
// lowercased and any character outside [a-z0-9-] becomes "-" so the
// result is a valid Kubernetes resource-name segment. Holds keys set
// at a specific env scope (e.g. preview-PR overrides).
func EnvSecretName(project, service, env string) string {
	safe := envSecretNameRE.ReplaceAllString(strings.ToLower(env), "-")
	return project + "-" + service + "-" + safe + "-secrets"
}
