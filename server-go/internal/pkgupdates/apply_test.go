package pkgupdates

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

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
