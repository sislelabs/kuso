package crons

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// kusocron renders image as `image: "<repository>:<tag>"` and
// `imagePullPolicy: <pullPolicy>` unquoted, so a quote or newline in any
// of the three injects extra manifests (a privileged DaemonSet in
// kube-system was reproducible).
var injectingImages = []kube.KusoImage{
	{Repository: "alpine\"\n---\napiVersion: apps/v1\nkind: DaemonSet", Tag: "3"},
	{Repository: "alpine", Tag: "3\"\n---\nkind: Pod"},
	{Repository: "alpine", Tag: "3", PullPolicy: "Always\n---\nkind: Pod"},
	{Repository: "alpine", Tag: "3", PullPolicy: "Sometimes"},
	{Repository: "alp ine", Tag: "3"},
	{Repository: "{{ .Release.Name }}", Tag: "3"},
}

func TestAddProject_RejectsInjectingImage(t *testing.T) {
	for _, img := range injectingImages {
		img := img
		s := cronFakeService(t)
		_, err := s.AddProject(context.Background(), "alpha", CreateProjectCronRequest{
			Name: "job", Kind: "command", Schedule: "0 * * * *",
			Command: []string{"true"}, Image: &img,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("image %+v: got %v, want ErrInvalid", img, err)
		}
	}
}

func TestUpdateProject_RejectsInjectingImage(t *testing.T) {
	existing := &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-job", Namespace: "kuso",
			Labels: map[string]string{"kuso.sislelabs.com/project": "alpha"}},
		Spec: kube.KusoCronSpec{Project: "alpha", Kind: "command", Schedule: "0 * * * *",
			Image: &kube.KusoImage{Repository: "alpine", Tag: "3"}},
	}
	for _, img := range injectingImages {
		img := img
		s := cronFakeService(t, existing)
		_, err := s.UpdateProject(context.Background(), "alpha", "job", UpdateProjectCronRequest{Image: &img})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("image %+v: got %v, want ErrInvalid", img, err)
		}
	}
}

func TestAddProject_AcceptsRealImageRefs(t *testing.T) {
	for _, img := range []kube.KusoImage{
		{Repository: "alpine", Tag: "3.20"},
		{Repository: "ghcr.io/sislelabs/kuso-backup", Tag: "latest", PullPolicy: "Always"},
		{Repository: "registry.local:5000/team/app", Tag: "main-mucrejj6", PullPolicy: "IfNotPresent"},
		{Repository: "busybox"},
	} {
		img := img
		s := cronFakeService(t)
		if _, err := s.AddProject(context.Background(), "alpha", CreateProjectCronRequest{
			Name: "job", Kind: "command", Schedule: "0 * * * *",
			Command: []string{"true"}, Image: &img,
		}); err != nil {
			t.Errorf("image %+v: unexpected error %v", img, err)
		}
	}
}
