package vulnscan

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

const (
	dA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	dC = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func ctrl(kind, name string) metav1.OwnerReference {
	yes := true
	return metav1.OwnerReference{Kind: kind, Name: name, Controller: &yes}
}

func pod(ns, name string, labels map[string]string, owner metav1.OwnerReference, image, imageID string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels, OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: image}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "main", Image: image, ImageID: imageID},
		}},
	}
}

func TestClassify(t *testing.T) {
	envs := EnvIndex([]kube.KusoEnvironment{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "kuso", Name: "shop-web-production", Labels: map[string]string{kube.LabelEnv: "production"}},
			Spec: kube.KusoEnvironmentSpec{Project: "shop", Service: "shop-web", Kind: "production"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "kuso", Name: "shop-web-pr-7", Labels: map[string]string{kube.LabelEnv: "preview-pr-7"}},
			Spec: kube.KusoEnvironmentSpec{Project: "shop", Service: "shop-web", Kind: "preview"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-ns", Name: "shop-api-staging", Labels: map[string]string{kube.LabelEnv: "staging"}},
			Spec: kube.KusoEnvironmentSpec{Project: "shop", Service: "shop-api"}},
	})
	envLabels := func(instance string) map[string]string {
		return map[string]string{"app.kubernetes.io/name": "kusoenvironment", "app.kubernetes.io/instance": instance,
			kube.LabelProject: "shop", "pod-template-hash": "5d9f"}
	}
	svcPod := pod("kuso", "shop-web-production-5d9f-x", envLabels("shop-web-production"), ctrl("ReplicaSet", "shop-web-production-5d9f"),
		"kuso-registry.kuso.svc.cluster.local:5000/shop/web:main-1", "kuso-registry.kuso.svc.cluster.local:5000/shop/web@"+dA)
	svcPod.Spec.InitContainers = []corev1.Container{{Name: "wait", Image: "busybox:1.36"}}
	svcPod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "wait", Image: "docker.io/library/busybox:1.36", ImageID: "docker.io/library/busybox@" + dC}}
	svcPod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "regcred"}}
	pods := []corev1.Pod{
		svcPod,
		pod("kuso", "shop-web-pr-7-abc-y", envLabels("shop-web-pr-7"), ctrl("ReplicaSet", "shop-web-pr-7-abc"), "r/web:pr", "r/web@"+dB),
		pod("tenant-ns", "shop-api-staging-1", envLabels("shop-api-staging"), ctrl("ReplicaSet", "shop-api-staging-5d9f"), "ghcr.io/acme/api:1", "ghcr.io/acme/api@"+dB),
		pod("kuso", "shop-db-0", map[string]string{"kuso.sislelabs.com/addon": "shop-db", kube.LabelProject: "shop"}, ctrl("StatefulSet", "shop-db"), "postgres:16", "docker.io/library/postgres@"+dB),
		pod("kuso", "kuso-server-77-z", map[string]string{"app": "kuso-server", "pod-template-hash": "77"}, ctrl("ReplicaSet", "kuso-server-77"), "ghcr.io/sislelabs/kuso-server:v1", "ghcr.io/sislelabs/kuso-server@"+dC),
		pod("kuso", "kuso-vulnscan-1-q", map[string]string{"app.kubernetes.io/name": "kuso-vulnscan"}, ctrl("Job", "kuso-vulnscan-1"), "aquasec/trivy", "x@"+dA),
		pod("kuso", "shop-web-abc123-build", map[string]string{kube.LabelProject: "shop", "app.kubernetes.io/component": "kusobuild"}, ctrl("Job", "b"), "moby/buildkit", "x@"+dA),
		pod("kube-system", "traefik-1", map[string]string{"app.kubernetes.io/name": "traefik", "pod-template-hash": "1"}, ctrl("ReplicaSet", "traefik-1"), "rancher/mirrored-library-traefik:3", "docker.io/rancher/mirrored-library-traefik@"+dB),
		pod("kube-system", "coredns-1", map[string]string{"k8s-app": "kube-dns"}, ctrl("ReplicaSet", "coredns-1"), "coredns", "x@"+dA),
		pod("someone-else", "random", nil, ctrl("ReplicaSet", "random"), "nginx", "x@"+dA),
		pod("kuso", "no-digest-yet", map[string]string{"app": "x"}, ctrl("DaemonSet", "x"), "nginx", ""),
	}
	got := Classify(pods, envs)

	type key struct{ kind, project, service, env, addon, ns, workload, digest string }
	have := map[key]db.VulnImage{}
	for _, im := range got {
		have[key{im.OwnerKind, im.Project, im.Service, im.Env, im.Addon, im.Namespace, im.Workload, im.Digest}] = im
	}
	want := []key{
		{"service", "shop", "web", "production", "", "kuso", "shop-web-production", dA},
		{"service", "shop", "web", "production", "", "kuso", "shop-web-production", dC}, // init container
		{"service", "shop", "api", "staging", "", "tenant-ns", "shop-api-staging", dB},
		{"addon", "shop", "", "", "db", "kuso", "shop-db", dB},
		{"platform", "", "", "", "", "kuso", "kuso-server", dC},
		{"platform", "", "", "", "", "kube-system", "traefik", dB},
	}
	for _, k := range want {
		if _, ok := have[k]; !ok {
			t.Errorf("missing %+v", k)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d rows, want %d (preview, job, build, coredns, foreign and digestless pods are skipped): %+v", len(got), len(want), got)
	}
	web := have[want[0]]
	if web.ImageRef != "kuso-registry.kuso.svc.cluster.local:5000/shop/web@"+dA || web.PullSecret != "kuso/regcred" {
		t.Errorf("service row = %+v", web)
	}
	if init := have[want[1]]; init.ImageRef != "busybox@"+dC {
		t.Errorf("init container ref = %q", init.ImageRef)
	}
}

func TestDigestOf(t *testing.T) {
	for in, want := range map[string]string{
		"docker-pullable://nginx@" + dA: dA,
		"ghcr.io/a/b@" + dA:             dA,
		dA:                              dA,
		"sha256:short":                  "",
		"":                              "",
	} {
		if got := digestOf(in); got != want {
			t.Errorf("digestOf(%q) = %q, want %q", in, got, want)
		}
	}
}
