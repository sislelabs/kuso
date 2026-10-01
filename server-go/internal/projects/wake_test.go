package projects

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"
)

func TestWakeServiceEnv_WakesRequestedEnvAndStampsActivity(t *testing.T) {
	t.Parallel()
	zero := int32(0)
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "staging", "dev", "alpha-web-staging"),
	)
	s.Kube.Clientset = k8sfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-staging", Namespace: "kuso"},
		Spec:       appsv1.DeploymentSpec{Replicas: &zero},
	})
	ctx := context.Background()
	if _, err := s.Kube.UpdateKusoEnvironmentWithRetry(ctx, "kuso", "alpha-web-staging", func(e *kube.KusoEnvironment) error {
		e.Annotations = map[string]string{scaledown.PreSleepReplicasAnnotation: "2"}
		e.Spec.SetReplicaCount(0)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.WakeServiceEnv(ctx, "alpha", "web", "staging"); err != nil {
		t.Fatalf("WakeServiceEnv: %v", err)
	}
	env, _ := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-staging")
	if got := env.Spec.ReplicaCountValue(); got != 2 {
		t.Fatalf("staging replicaCount = %d, want pre-sleep 2", got)
	}
	if env.Annotations[scaledown.LastActivityAnnotation] == "" {
		t.Fatal("last-activity not stamped; scaledown would re-sleep on the next tick")
	}
	dep, _ := s.Kube.Clientset.AppsV1().Deployments("kuso").Get(ctx, "alpha-web-staging", metav1.GetOptions{})
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 2 {
		t.Fatalf("deployment replicas = %v, want 2 (HPA envs only wake via the Deployment)", dep.Spec.Replicas)
	}
}

func TestWakeServiceEnv_RefusesStoppedService(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Stopped: true}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	s.Kube.Clientset = k8sfake.NewSimpleClientset()
	if err := s.WakeService(context.Background(), "alpha", "web"); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestWakeServiceEnv_UnknownEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
	)
	s.Kube.Clientset = k8sfake.NewSimpleClientset()
	if err := s.WakeServiceEnv(context.Background(), "alpha", "web", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
