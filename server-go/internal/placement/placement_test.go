package placement

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func TestMatches(t *testing.T) {
	cases := []struct {
		name      string
		placement *kube.KusoPlacement
		nodeName  string
		labels    map[string]string
		want      bool
	}{
		{
			name:      "nil placement matches anything",
			placement: nil,
			nodeName:  "node-a",
			labels:    nil,
			want:      true,
		},
		{
			name:      "empty placement matches anything",
			placement: &kube.KusoPlacement{},
			nodeName:  "node-a",
			labels:    map[string]string{"kuso.sislelabs.com/role": "web"},
			want:      true,
		},
		{
			name:      "single label match (prefix applied)",
			placement: &kube.KusoPlacement{Labels: map[string]string{"role": "web"}},
			nodeName:  "node-a",
			labels:    map[string]string{"kuso.sislelabs.com/role": "web"},
			want:      true,
		},
		{
			name:      "label value mismatch",
			placement: &kube.KusoPlacement{Labels: map[string]string{"role": "web"}},
			nodeName:  "node-a",
			labels:    map[string]string{"kuso.sislelabs.com/role": "db"},
			want:      false,
		},
		{
			name:      "label key absent on node",
			placement: &kube.KusoPlacement{Labels: map[string]string{"role": "web"}},
			nodeName:  "node-a",
			labels:    map[string]string{"kuso.sislelabs.com/zone": "eu"},
			want:      false,
		},
		{
			name: "AND-of-labels: all present matches",
			placement: &kube.KusoPlacement{Labels: map[string]string{
				"role": "web", "zone": "eu",
			}},
			nodeName: "node-a",
			labels: map[string]string{
				"kuso.sislelabs.com/role": "web",
				"kuso.sislelabs.com/zone": "eu",
			},
			want: true,
		},
		{
			name: "AND-of-labels: one missing fails",
			placement: &kube.KusoPlacement{Labels: map[string]string{
				"role": "web", "zone": "eu",
			}},
			nodeName: "node-a",
			labels: map[string]string{
				"kuso.sislelabs.com/role": "web",
			},
			want: false,
		},
		{
			name:      "unprefixed node label does not satisfy",
			placement: &kube.KusoPlacement{Labels: map[string]string{"role": "web"}},
			nodeName:  "node-a",
			labels:    map[string]string{"role": "web"}, // missing prefix
			want:      false,
		},
		{
			name:      "presence flag: empty value matches key present with any value",
			placement: &kube.KusoPlacement{Labels: map[string]string{"gpu": ""}},
			nodeName:  "node-a",
			labels:    map[string]string{"kuso.sislelabs.com/gpu": "true"},
			want:      true,
		},
		{
			name:      "presence flag: empty value matches key present with empty value",
			placement: &kube.KusoPlacement{Labels: map[string]string{"gpu": ""}},
			nodeName:  "node-a",
			labels:    map[string]string{"kuso.sislelabs.com/gpu": ""},
			want:      true,
		},
		{
			name:      "presence flag: absent key still fails",
			placement: &kube.KusoPlacement{Labels: map[string]string{"gpu": ""}},
			nodeName:  "node-a",
			labels:    map[string]string{"kuso.sislelabs.com/role": "web"},
			want:      false,
		},
		{
			name: "presence flag AND value label: both must hold",
			placement: &kube.KusoPlacement{Labels: map[string]string{
				"gpu": "", "role": "web",
			}},
			nodeName: "node-a",
			labels: map[string]string{
				"kuso.sislelabs.com/gpu":  "a100",
				"kuso.sislelabs.com/role": "web",
			},
			want: true,
		},
		{
			name: "presence flag AND value label: value mismatch fails",
			placement: &kube.KusoPlacement{Labels: map[string]string{
				"gpu": "", "role": "web",
			}},
			nodeName: "node-a",
			labels: map[string]string{
				"kuso.sislelabs.com/gpu":  "a100",
				"kuso.sislelabs.com/role": "db",
			},
			want: false,
		},
		{
			name:      "node whitelist hit",
			placement: &kube.KusoPlacement{Nodes: []string{"node-a", "node-b"}},
			nodeName:  "node-b",
			labels:    nil,
			want:      true,
		},
		{
			name:      "node whitelist miss",
			placement: &kube.KusoPlacement{Nodes: []string{"node-a", "node-b"}},
			nodeName:  "node-c",
			labels:    nil,
			want:      false,
		},
		{
			name: "labels AND nodes both must pass: node miss fails despite label match",
			placement: &kube.KusoPlacement{
				Labels: map[string]string{"role": "web"},
				Nodes:  []string{"node-a"},
			},
			nodeName: "node-b",
			labels:   map[string]string{"kuso.sislelabs.com/role": "web"},
			want:     false,
		},
		{
			name: "labels AND nodes both must pass: label miss fails despite node hit",
			placement: &kube.KusoPlacement{
				Labels: map[string]string{"role": "web"},
				Nodes:  []string{"node-a"},
			},
			nodeName: "node-a",
			labels:   map[string]string{"kuso.sislelabs.com/role": "db"},
			want:     false,
		},
		{
			name: "labels AND nodes both pass",
			placement: &kube.KusoPlacement{
				Labels: map[string]string{"role": "web"},
				Nodes:  []string{"node-a"},
			},
			nodeName: "node-a",
			labels:   map[string]string{"kuso.sislelabs.com/role": "web"},
			want:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(tc.placement, tc.nodeName, tc.labels); got != tc.want {
				t.Errorf("Matches() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchesSchedulable_SkipsCordonedAndNotReady(t *testing.T) {
	p := &kube.KusoPlacement{Nodes: []string{"n1"}}
	ready := corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}
	notReady := corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}}}

	ok := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: ready}
	if !MatchesSchedulable(p, ok) {
		t.Fatal("ready, uncordoned node should match")
	}
	cordoned := ok.DeepCopy()
	cordoned.Spec.Unschedulable = true
	if MatchesSchedulable(p, cordoned) {
		t.Fatal("cordoned node must not match")
	}
	down := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: notReady}
	if MatchesSchedulable(p, down) {
		t.Fatal("NotReady node must not match")
	}
}
