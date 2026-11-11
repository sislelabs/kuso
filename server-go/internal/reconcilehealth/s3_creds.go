package reconcilehealth

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// KindOrphanS3Secret: an s3 addon's kept -root or -readonly Secret whose
// KusoAddon is gone. Same-name re-creation reads the old root password
// back through the chart's lookup, so the leftover is a live credential
// in waiting, not just clutter (BLOB-8).
const KindOrphanS3Secret Kind = "orphan_s3_secret"

// KindS3ConnHoldsRoot: an s3 addon created before scoped credentials
// still hands MinIO's root key to every subscribed pod (BLOB-3).
const KindS3ConnHoldsRoot Kind = "s3_conn_holds_root"

const labelRole = "kuso.sislelabs.com/role"

var s3CredentialRoles = map[string]bool{"s3-root": true, "s3-readonly": true}

func isS3CredentialSecret(sec *corev1.Secret) bool {
	return s3CredentialRoles[sec.Labels[labelRole]] && sec.Labels[labelAddonInstance] != ""
}

// detectOrphanS3Secrets keys on the absence of the addon CR named by the
// chart's instance label, like the conn and PVC sweeps.
func detectOrphanS3Secrets(secrets []corev1.Secret, live map[string]bool) []Issue {
	var out []Issue
	for i := range secrets {
		sec := &secrets[i]
		if !isS3CredentialSecret(sec) || sec.DeletionTimestamp != nil {
			continue
		}
		addonCR := sec.Labels[labelAddonInstance]
		if live[addonCR] {
			continue
		}
		out = append(out, Issue{
			Resource:  sec.Name,
			Namespace: sec.Namespace,
			Project:   sec.Labels["kuso.sislelabs.com/project"],
			Type:      "addon",
			AddonKind: "s3",
			Kind:      KindOrphanS3Secret,
			Severity:  SeverityWarning,
			Summary:   fmt.Sprintf("Secret %s has no KusoAddon %q: a deleted S3 addon's MinIO credential survived.", sec.Name, addonCR),
			Detail: "Re-creating an addon with this name reuses the password in this Secret. " +
				"If the addon's data volume is gone too, nothing needs it.",
			Action:     ActionNone,
			Fix:        "Delete this Secret unless you are about to restore the addon over its retained volume.",
			RunbookCmd: fmt.Sprintf("kuso health orphans delete --kind %s --namespace %s --name %s", KindOrphanS3Secret, sec.Namespace, sec.Name),
		})
	}
	return out
}

// s3ConnHoldsRoot reports an in-cluster s3 addon whose conn Secret still
// carries the root key. Reads two Secrets live (the cache strips data);
// any read failure, or a root Secret the chart has not written yet,
// reports nothing.
func (s *Scanner) s3ConnHoldsRoot(ctx context.Context, a *kube.KusoAddon) (Issue, bool) {
	if s.Kube == nil || s.Kube.Clientset == nil || a.Spec.Kind != "s3" || a.Spec.External != nil || a.Spec.UseInstanceAddon != "" {
		return Issue{}, false
	}
	secrets := s.Kube.Clientset.CoreV1().Secrets(a.Namespace)
	root, err := secrets.Get(ctx, a.Name+"-root", metav1.GetOptions{})
	if err != nil {
		return Issue{}, false
	}
	conn, err := secrets.Get(ctx, a.Name+connSecretSuffix, metav1.GetOptions{})
	if err != nil {
		return Issue{}, false
	}
	rootUser := string(root.Data["MINIO_ROOT_USER"])
	if rootUser == "" || string(conn.Data["S3_ACCESS_KEY_ID"]) != rootUser {
		return Issue{}, false
	}
	project := a.Spec.Project
	if project == "" {
		project = a.Labels[kube.LabelProject]
	}
	short := strings.TrimPrefix(a.Name, project+"-")
	return Issue{
		Resource:  a.Name,
		Namespace: a.Namespace,
		Project:   project,
		Type:      "addon",
		AddonKind: "s3",
		Kind:      KindS3ConnHoldsRoot,
		Severity:  SeverityWarning,
		Summary:   "S3 addon hands MinIO's root key to every subscribed service and preview.",
		Detail: "The addon predates bucket-scoped credentials. Any pod, preview or log that saw " +
			a.Name + "-conn holds full admin over this MinIO. A scoped key reaches only S3_BUCKET: " +
			"if the app uses other buckets on this MinIO, keep root until it doesn't.",
		Action: ActionNone,
		Fix: "Rotate to a bucket-scoped key and a new root password, then restart the services using " +
			a.Name + "-conn.",
		RunbookCmd: fmt.Sprintf("kuso project addon s3 rotate-credentials %s %s --root", project, short),
	}, true
}
