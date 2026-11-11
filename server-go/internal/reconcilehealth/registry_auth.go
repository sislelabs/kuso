package reconcilehealth

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/registryauth"
)

// KindRegistryAuthDisabled: the in-cluster registry still serves anonymously
// (an install that predates registry auth and never switched it on), so any
// pod can pull, push over or delete every project's images (BLOB-R5).
const KindRegistryAuthDisabled Kind = "registry_auth_disabled"

// registryAuthOff reads kuso-registry-auth live. A read error reports
// nothing: advisory scan, fail silent-healthy.
func (s *Scanner) registryAuthOff(ctx context.Context, ns string) (Issue, bool) {
	if s.Kube == nil || s.Kube.Clientset == nil {
		return Issue{}, false
	}
	sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, registryauth.SecretName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return Issue{}, false
	}
	if err == nil && string(sec.Data["enabled"]) == "true" {
		return Issue{}, false
	}
	return Issue{
		Resource:  "kuso-registry",
		Namespace: ns,
		Type:      "platform",
		Kind:      KindRegistryAuthDisabled,
		Severity:  SeverityWarning,
		Summary:   "The in-cluster image registry accepts anonymous pulls, pushes and deletes.",
		Detail: "Any pod on the cluster network can read every project's images, overwrite a tag or delete a " +
			"manifest (the next restart of that service then fails ImagePullBackOff).",
		Action:     ActionNone,
		Fix:        "Turn on registry auth: follow the 5 steps in the header of deploy/registry.yaml.",
		RunbookCmd: "kuso api GET admin/registry-auth",
	}, true
}
