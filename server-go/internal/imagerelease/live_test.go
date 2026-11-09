package imagerelease

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestRolledOut(t *testing.T) {
	t.Parallel()
	two := int32(2)
	dep := func(img string, ready, updated, total int32) *appsv1.Deployment {
		d := &appsv1.Deployment{}
		d.Spec.Replicas = &two
		d.Spec.Template.Spec.Containers = []corev1.Container{{Image: img}}
		d.Status = appsv1.DeploymentStatus{ReadyReplicas: ready, UpdatedReplicas: updated, Replicas: total}
		return d
	}
	cases := []struct {
		name string
		d    *appsv1.Deployment
		want bool
	}{
		{"all ready on the image", dep("nginx:1.27", 2, 2, 2), true},
		{"pull never succeeded", dep("nginx:nope", 0, 1, 3), false},
		{"old pods still around", dep("nginx:1.27", 2, 2, 3), false},
		{"different image", dep("nginx:1.28", 2, 2, 2), false},
		{"no deployment", nil, false},
	}
	for _, c := range cases {
		if got := rolledOut(c.d, "nginx:1.27"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
