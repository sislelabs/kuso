package pkgupdates

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// slogDiscard returns a logger that drops output, for tests that only
// assert on cluster state.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBuildApplyJob(t *testing.T) {
	t.Parallel()
	w := &Watcher{}

	// allowReboot=false → ALLOW_REBOOT env "false", pinned to node,
	// privileged, reuses the probe SA.
	j := w.buildApplyJob("server2", "apt", false)
	if j.Name != "kuso-pkg-apply-server2" || j.Namespace != "kuso" {
		t.Errorf("job meta: %s/%s", j.Namespace, j.Name)
	}
	pod := j.Spec.Template.Spec
	if pod.NodeName != "server2" {
		t.Errorf("not pinned to node: %q", pod.NodeName)
	}
	if !pod.HostPID {
		t.Error("hostPID must be true (nsenter host)")
	}
	if pod.ServiceAccountName != "kuso-pkg-probe" {
		t.Errorf("SA = %q, want kuso-pkg-probe", pod.ServiceAccountName)
	}
	c := pod.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.Privileged == nil || !*c.SecurityContext.Privileged {
		t.Error("container must be privileged")
	}
	if envVal(c.Env, "ALLOW_REBOOT") != "false" || envVal(c.Env, "PKG_MGR") != "apt" {
		t.Errorf("env: ALLOW_REBOOT=%q PKG_MGR=%q", envVal(c.Env, "ALLOW_REBOOT"), envVal(c.Env, "PKG_MGR"))
	}

	// allowReboot=true → ALLOW_REBOOT "true".
	if envVal(w.buildApplyJob("n", "apt", true).Spec.Template.Spec.Containers[0].Env, "ALLOW_REBOOT") != "true" {
		t.Error("allowReboot=true should set ALLOW_REBOOT=true")
	}
}

func envVal(env []corev1.EnvVar, k string) string {
	for _, e := range env {
		if e.Name == k {
			return e.Value
		}
	}
	return ""
}

func TestAnotherNodeRebooting(t *testing.T) {
	t.Parallel()
	mkNode := func(name string, ann map[string]string) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: ann},
		}
	}
	applyState := func(phase string) map[string]string {
		return map[string]string{ApplyStateAnnotation: `{"phase":"` + phase + `","at":"","log":""}`}
	}

	cases := []struct {
		name   string
		nodes  []*corev1.Node
		except string
		want   string // expected busy node name, "" = none
	}{
		{
			name:   "no other node busy",
			nodes:  []*corev1.Node{mkNode("a", nil), mkNode("b", nil)},
			except: "a",
			want:   "",
		},
		{
			name: "other node carries our cordon marker",
			nodes: []*corev1.Node{
				mkNode("a", nil),
				mkNode("b", map[string]string{CordonAnnotation: "true"}),
			},
			except: "a",
			want:   "b",
		},
		{
			name: "other node draining",
			nodes: []*corev1.Node{
				mkNode("a", nil),
				mkNode("b", applyState("draining")),
			},
			except: "a",
			want:   "b",
		},
		{
			name: "other node rebooting",
			nodes: []*corev1.Node{
				mkNode("a", nil),
				mkNode("b", applyState("rebooting")),
			},
			except: "a",
			want:   "b",
		},
		{
			// Regression: settling must count as busy. Node b is back +
			// uncordoned but its reboot-stranded singletons are still
			// rescheduling; starting node a's drain now risks evicting the
			// last healthy replica of a service mid-reschedule on b.
			name: "other node settling",
			nodes: []*corev1.Node{
				mkNode("a", nil),
				mkNode("b", applyState("settling")),
			},
			except: "a",
			want:   "b",
		},
		{
			name: "other node running (apply job in flight)",
			nodes: []*corev1.Node{
				mkNode("a", nil),
				mkNode("b", applyState("running")),
			},
			except: "a",
			want:   "b",
		},
		{
			name: "the excepted node itself being busy does not count",
			nodes: []*corev1.Node{
				mkNode("a", map[string]string{CordonAnnotation: "true"}),
				mkNode("b", nil),
			},
			except: "a",
			want:   "",
		},
		{
			name: "a done node is not busy",
			nodes: []*corev1.Node{
				mkNode("a", nil),
				mkNode("b", applyState("done")),
			},
			except: "a",
			want:   "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			objs := make([]runtime.Object, 0, len(c.nodes))
			for _, n := range c.nodes {
				objs = append(objs, n)
			}
			w := &Watcher{Kube: &kube.Client{Clientset: kubefake.NewSimpleClientset(objs...)}}
			got, err := w.anotherNodeRebooting(context.Background(), c.except)
			if err != nil {
				t.Fatalf("anotherNodeRebooting: %v", err)
			}
			if got != c.want {
				t.Errorf("busy node = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRescheduleStrandedPods(t *testing.T) {
	t.Parallel()
	mkPod := func(name string, phase corev1.PodPhase, node string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
			Spec:       corev1.PodSpec{NodeName: node},
			Status:     corev1.PodStatus{Phase: phase},
		}
	}
	objs := []runtime.Object{
		mkPod("stranded", corev1.PodUnknown, "worker"), // should be deleted
		mkPod("healthy", corev1.PodRunning, "worker"),  // should survive
		mkPod("pending", corev1.PodPending, "worker"),  // should survive
	}
	cs := kubefake.NewSimpleClientset(objs...)
	w := &Watcher{Kube: &kube.Client{Clientset: cs}}
	w.rescheduleStrandedPods(context.Background(), "worker", slogDiscard())

	remaining, err := cs.CoreV1().Pods("kuso").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	got := map[string]bool{}
	for _, p := range remaining.Items {
		got[p.Name] = true
	}
	if got["stranded"] {
		t.Error("Unknown-phase pod should have been force-deleted")
	}
	if !got["healthy"] || !got["pending"] {
		t.Error("Running/Pending pods must not be touched")
	}
}

func TestReconcileRebootsUncordonsAndSweeps(t *testing.T) {
	t.Parallel()
	readyNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker",
			Annotations: map[string]string{
				CordonAnnotation:     "true",
				ApplyStateAnnotation: `{"phase":"rebooting","at":"","log":""}`,
			},
		},
		Spec: corev1.NodeSpec{Unschedulable: true},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}},
	}
	strandedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pooler", Namespace: "kuso"},
		Spec:       corev1.PodSpec{NodeName: "worker"},
		Status:     corev1.PodStatus{Phase: corev1.PodUnknown},
	}
	cs := kubefake.NewSimpleClientset(readyNode, strandedPod)
	w := &Watcher{Kube: &kube.Client{Clientset: cs}}
	w.reconcileReboots(context.Background(), slogDiscard())

	n, err := cs.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if n.Spec.Unschedulable {
		t.Error("node should have been uncordoned after reboot")
	}
	if n.Annotations[CordonAnnotation] != "" {
		t.Errorf("cordon marker should be cleared, got %q", n.Annotations[CordonAnnotation])
	}
	if _, err := cs.CoreV1().Pods("kuso").Get(context.Background(), "pooler", metav1.GetOptions{}); err == nil {
		t.Error("stranded pooler pod should have been force-deleted")
	}
}

// TestReconcileRebootsSweepsLateStrandedPod covers the gap that stranded
// distill-db-pooler on server2: a pod can flip to Unknown AFTER the node
// returns Ready, so the finalize must keep sweeping (via the `settling`
// phase) rather than declaring done on the first pass.
func TestReconcileRebootsSweepsLateStrandedPod(t *testing.T) {
	t.Parallel()
	nowRFC := func() string { return time.Now().UTC().Format(time.RFC3339) }
	readyNode := func() *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "server2",
				Annotations: map[string]string{
					CordonAnnotation:     "true",
					ApplyStateAnnotation: `{"phase":"rebooting","at":"` + nowRFC() + `","log":""}`,
				},
			},
			Spec: corev1.NodeSpec{Unschedulable: true},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			}},
		}
	}
	cs := kubefake.NewSimpleClientset(readyNode())
	w := &Watcher{Kube: &kube.Client{Clientset: cs}}

	// Pass 1: node just back, no Unknown pods yet. Must uncordon and enter
	// `settling` (NOT done) so it keeps watching.
	w.reconcileReboots(context.Background(), slogDiscard())
	n, _ := cs.CoreV1().Nodes().Get(context.Background(), "server2", metav1.GetOptions{})
	if n.Spec.Unschedulable {
		t.Fatal("pass 1: node should be uncordoned")
	}
	if got := parseApplyState(n.Annotations[ApplyStateAnnotation]).Phase; got != "settling" {
		t.Fatalf("pass 1: phase = %q, want settling (must not go straight to done)", got)
	}

	// A pod NOW strands in Unknown (late), the way distill-db-pooler did.
	_, _ = cs.CoreV1().Pods("kuso").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pooler", Namespace: "kuso"},
		Spec:       corev1.PodSpec{NodeName: "server2"},
		Status:     corev1.PodStatus{Phase: corev1.PodUnknown},
	}, metav1.CreateOptions{})

	// Pass 2: still in `settling`, so the sweep must run and delete it.
	w.reconcileReboots(context.Background(), slogDiscard())
	if _, err := cs.CoreV1().Pods("kuso").Get(context.Background(), "pooler", metav1.GetOptions{}); err == nil {
		t.Error("pass 2: late-stranded pod should have been swept during settling")
	}
}

// TestReconcileRebootsSkipsNodeMidApply covers the P1 bug: Apply() stamps
// our cordon marker UP FRONT (before the Job's apt/drain runs), so a node
// in phase=running or =draining is a healthy, cordoned, Ready node that is
// STILL doing the apply work. The 15s finalize tick must NOT uncordon it,
// clear the marker, or stomp its phase — that would let scheduling resume
// and drop the phase toward done while apt/drain is mid-flight.
func TestReconcileRebootsSkipsNodeMidApply(t *testing.T) {
	t.Parallel()
	mkNode := func(phase string) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "worker",
				Annotations: map[string]string{
					CordonAnnotation:     "true", // stamped up-front by Apply()
					ApplyStateAnnotation: `{"phase":"` + phase + `","at":"","log":""}`,
				},
			},
			// Cordoned (unschedulable) + Ready: exactly the mid-apply state.
			Spec: corev1.NodeSpec{Unschedulable: true},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			}},
		}
	}

	for _, phase := range []string{"running", "draining"} {
		t.Run(phase, func(t *testing.T) {
			cs := kubefake.NewSimpleClientset(mkNode(phase))
			w := &Watcher{Kube: &kube.Client{Clientset: cs}}
			w.reconcileReboots(context.Background(), slogDiscard())

			n, err := cs.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get node: %v", err)
			}
			if !n.Spec.Unschedulable {
				t.Errorf("phase=%s: node was uncordoned mid-apply (must stay cordoned)", phase)
			}
			if n.Annotations[CordonAnnotation] != "true" {
				t.Errorf("phase=%s: cordon marker was cleared mid-apply", phase)
			}
			if got := parseApplyState(n.Annotations[ApplyStateAnnotation]).Phase; got != phase {
				t.Errorf("phase=%s: apply-state was stomped to %q mid-apply", phase, got)
			}
		})
	}
}
