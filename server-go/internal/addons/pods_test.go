package addons

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// An addon that renders a Deployment but never runs a pod is invisible today:
// `kuso service pods` only covers services, `kuso health` counts the addon
// healthy because its Helm release deployed fine, and the only symptom is
// connection-refused from whatever tried to use it. ListPods closes that —
// it has to surface WHY a pod isn't running, not just that none are.

func addonPodsService(t *testing.T, pods ...*corev1.Pod) *Service {
	t.Helper()
	s := fakeService(t, seedProj("alpha"), seedAddon("alpha", "pg", "postgres"))
	objs := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		objs = append(objs, p)
	}
	s.Kube.Clientset = kubefake.NewSimpleClientset(objs...)
	return s
}

func TestListAddonPods_SurfacesPendingReason(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-pg-pooler-abc",
			Namespace: "kuso",
			Labels:    map[string]string{"app.kubernetes.io/instance": "alpha-pg"},
		},
		Status: corev1.PodStatus{
			Phase:  corev1.PodPending,
			Reason: "SchedulingGated",
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: `no PriorityClass named "kuso-platform"`,
			}},
		},
	}
	s := addonPodsService(t, pod)

	out, err := s.ListPods(context.Background(), "alpha", "pg")
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	if len(out.Pods) != 1 {
		t.Fatalf("pods = %d, want 1", len(out.Pods))
	}
	p := out.Pods[0]
	if p.Phase != "Pending" {
		t.Errorf("phase = %q, want Pending", p.Phase)
	}
	// The whole point: a pod that never starts must explain itself.
	if p.Message == "" {
		t.Error("a Pending pod reported no message — the scheduler's reason is " +
			"the only thing that explains why the addon has no endpoints")
	}
}

func TestListAddonPods_SurfacesWaitingContainerReason(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-pg-pooler-def",
			Namespace: "kuso",
			Labels:    map[string]string{"app.kubernetes.io/instance": "alpha-pg"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name: "render-userlist",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "CreateContainerConfigError",
					Message: "secret alpha-pg-conn missing key POSTGRES_USER",
				}},
			}},
		},
	}
	s := addonPodsService(t, pod)

	out, err := s.ListPods(context.Background(), "alpha", "pg")
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	if len(out.Pods) != 1 {
		t.Fatalf("pods = %d, want 1", len(out.Pods))
	}
	if out.Pods[0].Message == "" {
		t.Error("a stuck init container reported no message — this is the most " +
			"common addon-pod failure and it must not be silent")
	}
}

// Zero pods is itself the answer when a Deployment renders but nothing runs.
func TestListAddonPods_EmptyIsNotAnError(t *testing.T) {
	s := addonPodsService(t)

	out, err := s.ListPods(context.Background(), "alpha", "pg")
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	if len(out.Pods) != 0 {
		t.Errorf("pods = %d, want 0", len(out.Pods))
	}
}

// CRName tolerates a pre-qualified name, so a viewer of "foo" asking for
// "bar-db" or "foo-bar-db" resolves project foo-bar's addon.
func TestListAddonPods_RefusesOtherProjectsAddon(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-bar-db-0",
			Namespace: "kuso",
			Labels:    map[string]string{"app.kubernetes.io/instance": "foo-bar-db"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	s := fakeService(t, seedProj("foo"), seedProj("foo-bar"), seedAddon("foo-bar", "db", "postgres"))
	s.Kube.Clientset = kubefake.NewSimpleClientset(pod)

	for _, name := range []string{"bar-db", "foo-bar-db"} {
		out, err := s.ListPods(context.Background(), "foo", name)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ListPods(foo, %q): want ErrNotFound, got err=%v out=%+v", name, err, out)
		}
	}
	out, err := s.ListPods(context.Background(), "foo-bar", "db")
	if err != nil || len(out.Pods) != 1 {
		t.Fatalf("owner ListPods: err=%v out=%+v", err, out)
	}
}

func TestImageVersion(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"postgres:16-alpine": "16-alpine",
		"docker.redpanda.com/redpandadata/redpanda:v24.2.7": "v24.2.7",
		"registry:5000/redis:7.4-alpine":                    "7.4-alpine",
		"registry:5000/redis":                               "latest",
		"redis":                                             "latest",
		"postgres:16@sha256:" + sha:                         "16",
		"postgres@sha256:" + sha:                            "",
		"registry:5000/postgres@sha256:" + sha:              "",
		"":                                                  "",
	}
	for in, want := range cases {
		if got := imageVersion(in); got != want {
			t.Errorf("imageVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

const sha = "3f1e5c2a9b7d4e6f8a0c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f"

// The running version comes from the datastore pod, never the pooler.
func TestListAddonPods_RunningVersionFromMainPod(t *testing.T) {
	t.Parallel()
	ctrl := true
	mk := func(name, ownerKind, owner, image string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso",
				Labels:          map[string]string{"app.kubernetes.io/instance": "alpha-pg", "kuso.sislelabs.com/component": "x"},
				OwnerReferences: []metav1.OwnerReference{{Kind: ownerKind, Name: owner, Controller: &ctrl}}},
			Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: image}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
	}
	s := addonPodsService(t,
		mk("alpha-pg-0-pooler", "ReplicaSet", "alpha-pg-pooler-5d9f", "edoburu/pgbouncer:v1.25.1-p0"),
		mk("alpha-pg-0-sentinel", "StatefulSet", "alpha-pg-sentinel", "redis:7-alpine"),
		mk("alpha-pg-1", "StatefulSet", "alpha-pg", "postgres:16.4-alpine"),
	)
	out, err := s.ListPods(context.Background(), "alpha", "pg")
	if err != nil {
		t.Fatal(err)
	}
	if out.RunningImage != "postgres:16.4-alpine" || out.RunningVersion != "16.4-alpine" {
		t.Fatalf("running = %q / %q, want the postgres pod's image", out.RunningImage, out.RunningVersion)
	}
}
