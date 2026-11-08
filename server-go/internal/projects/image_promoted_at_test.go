package projects

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func TestNeedsPromotedAtBackfill(t *testing.T) {
	t.Parallel()
	img := &kube.KusoImage{Repository: "nginx", Tag: "1.27"}
	stamped := map[string]string{kube.AnnPromotedAt: time.Now().UTC().Format(time.RFC3339)}
	cases := []struct {
		name string
		env  kube.KusoEnvironment
		want bool
	}{
		{"image env without stamp", kube.KusoEnvironment{Spec: kube.KusoEnvironmentSpec{Runtime: "image", Image: img}}, true},
		{"already stamped", kube.KusoEnvironment{ObjectMeta: metav1.ObjectMeta{Annotations: stamped}, Spec: kube.KusoEnvironmentSpec{Runtime: "image", Image: img}}, false},
		{"built runtime", kube.KusoEnvironment{Spec: kube.KusoEnvironmentSpec{Runtime: "dockerfile", Image: img}}, false},
		{"image withheld behind a release hook", kube.KusoEnvironment{Spec: kube.KusoEnvironmentSpec{Runtime: "image", PendingImage: img}}, false},
	}
	for _, c := range cases {
		if got := needsPromotedAtBackfill(&c.env); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
