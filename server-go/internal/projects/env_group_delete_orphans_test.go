package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// Deleting a custom env group must leave nothing of it behind. Live on
// 2026-09-28, `kuso env-group delete scubatony verify` removed the env and
// addon CRs but left the three clone addons' data PVCs (+ Bound PVs holding
// the postgres/redis/s3 data), their *-conn Secrets (live credentials) and
// both envs' cert-manager TLS Secrets. DeleteEnvironment already reclaims
// the PVCs + TLS for a single env; DeleteEnvGroup bypassed all of it.
func TestDeleteEnvGroup_LeavesNoOrphans(t *testing.T) {
	const ns = "kuso"
	ctx := context.Background()

	pvc := func(name, instance string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels:      map[string]string{"app.kubernetes.io/instance": instance},
			Annotations: map[string]string{"helm.sh/resource-policy": "keep"},
		}}
	}
	secret := func(name string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	}

	var objs []runtime.Object
	for _, grp := range []string{"verify", "staging"} {
		for _, a := range []string{"db", "cache", "storage"} {
			fqn := "st-" + a + "-" + grp
			objs = append(objs, pvc("data-"+fqn+"-0", fqn), secret(fqn+"-conn"))
		}
		for _, svc := range []string{"web", "api"} {
			objs = append(objs, secret("st-"+svc+"-"+grp+"-tls"))
		}
	}
	for _, a := range []string{"db", "cache", "storage"} {
		objs = append(objs, pvc("data-st-"+a+"-0", "st-"+a), secret("st-"+a+"-conn"))
	}
	// Shared per-service secrets every env mounts — never the group's.
	objs = append(objs, secret("st-web-secrets"), secret("st-api-secrets"),
		secret("st-web-production-tls"), secret("st-web-verify-tls-extra-verify-example-com"))

	seeds := []seed{seedProject("st", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}})}
	for _, grp := range []string{"verify", "staging", "production"} {
		for _, svc := range []string{"web", "api"} {
			seeds = append(seeds, seedEnv("st", svc, grp, grp, "st-"+svc+"-"+grp))
		}
	}
	for _, grp := range []string{"verify", "staging"} {
		for _, a := range []string{"db", "cache", "storage"} {
			seeds = append(seeds, seedScopedAddon("st", "st-"+a+"-"+grp, map[string]string{labelEnv: grp}, ""))
		}
	}
	for _, a := range []string{"db", "cache", "storage"} {
		seeds = append(seeds, seedScopedAddon("st", "st-"+a, nil, ""))
	}
	s := fakeService(t, seeds...)
	cs := k8sfake.NewSimpleClientset(objs...)
	s.Kube.Clientset = cs

	if err := s.DeleteEnvGroup(ctx, "st", "verify"); err != nil {
		t.Fatalf("DeleteEnvGroup: %v", err)
	}

	pvcExists := func(name string) bool {
		_, err := cs.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
		return !apierrors.IsNotFound(err)
	}
	secretExists := func(name string) bool {
		_, err := cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		return !apierrors.IsNotFound(err)
	}

	for _, a := range []string{"db", "cache", "storage"} {
		if pvcExists("data-st-" + a + "-verify-0") {
			t.Errorf("verify clone PVC data-st-%s-verify-0 orphaned", a)
		}
		if secretExists("st-" + a + "-verify-conn") {
			t.Errorf("verify clone conn secret st-%s-verify-conn orphaned", a)
		}
		if addonExists(t, s, "st-"+a+"-verify") {
			t.Errorf("verify addon CR st-%s-verify survived", a)
		}
		// Siblings: staging clones and production data are untouched.
		for _, keep := range []string{"st-" + a + "-staging", "st-" + a} {
			if !pvcExists("data-"+keep+"-0") || !secretExists(keep+"-conn") || !addonExists(t, s, keep) {
				t.Errorf("%s (PVC, conn secret or CR) was collaterally deleted", keep)
			}
		}
	}
	for _, svc := range []string{"web", "api"} {
		if secretExists("st-" + svc + "-verify-tls") {
			t.Errorf("TLS secret st-%s-verify-tls orphaned", svc)
		}
		if !secretExists("st-"+svc+"-staging-tls") || !secretExists("st-"+svc+"-secrets") {
			t.Errorf("staging TLS or shared service secret for %s was deleted", svc)
		}
	}
	if secretExists("st-web-verify-tls-extra-verify-example-com") {
		t.Error("extra-host TLS secret orphaned")
	}
	if !secretExists("st-web-production-tls") {
		t.Error("production TLS secret deleted")
	}
}
