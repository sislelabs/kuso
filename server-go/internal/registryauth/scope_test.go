package registryauth

import "testing"

func TestForeignRepo(t *testing.T) {
	cases := []struct {
		repo string
		want bool
	}{
		{"kuso-registry.kuso.svc.cluster.local:5000/alpha/api", false},
		{"kuso-registry.kuso.svc.cluster.local:5000/alpha/api:v1", false},
		{"kuso-registry.kuso.svc.cluster.local:5000/beta/api", true},
		{"KUSO-REGISTRY.kuso.svc.cluster.local:5000/Beta/api", true},
		{"kuso-registry:5000/beta/api", true},
		{"kuso-registry.kuso/beta/api", true},
		{"http://kuso-registry.kuso.svc.cluster.local:5000/beta/api", true},
		{"ghcr.io/beta/api", false},
		{"beta/api", false},
		{"nginx", false},
		{"kuso-registryx.example.com/beta/api", false},
	}
	for _, c := range cases {
		if got := ForeignRepo("alpha", c.repo); got != c.want {
			t.Errorf("ForeignRepo(alpha, %q) = %v, want %v", c.repo, got, c.want)
		}
	}
}
