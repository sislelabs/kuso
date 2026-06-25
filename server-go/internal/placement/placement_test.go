package placement

import (
	"testing"

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

func TestCountMatches(t *testing.T) {
	nodes := []NodeIdentity{
		{Name: "web-1", Labels: map[string]string{"kuso.sislelabs.com/role": "web"}},
		{Name: "web-2", Labels: map[string]string{"kuso.sislelabs.com/role": "web"}},
		{Name: "db-1", Labels: map[string]string{"kuso.sislelabs.com/role": "db"}},
	}

	cases := []struct {
		name      string
		placement *kube.KusoPlacement
		nodes     []NodeIdentity
		want      int
	}{
		{"nil placement counts all", nil, nodes, 3},
		{"nil placement, empty nodes", nil, nil, 0},
		{"empty placement counts all", &kube.KusoPlacement{}, nodes, 3},
		{
			"label filter counts subset",
			&kube.KusoPlacement{Labels: map[string]string{"role": "web"}},
			nodes, 2,
		},
		{
			"unsatisfiable placement counts zero",
			&kube.KusoPlacement{Labels: map[string]string{"role": "cache"}},
			nodes, 0,
		},
		{
			"node whitelist counts subset",
			&kube.KusoPlacement{Nodes: []string{"db-1"}},
			nodes, 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountMatches(tc.placement, tc.nodes); got != tc.want {
				t.Errorf("CountMatches() = %d, want %d", got, tc.want)
			}
		})
	}
}
