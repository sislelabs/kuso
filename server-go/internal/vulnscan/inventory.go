package vulnscan

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// platformNamespaces hold kuso's own workloads. kube-system is handled
// separately: only k3s's bundled traefik there counts as platform.
var platformNamespaces = map[string]bool{"kuso": true, "kuso-operator-system": true, "traefik": true}

const labelAddon = kube.LabelPrefix + "addon"

// EnvInfo is what classification needs from one KusoEnvironment.
type EnvInfo struct {
	Project string
	Service string // short name
	Env     string // env group: production, staging, preview-pr-7
	Preview bool
}

// EnvIndex keys environments by "<namespace>/<env CR name>", which is
// the app.kubernetes.io/instance label on their pods.
func EnvIndex(envs []kube.KusoEnvironment) map[string]EnvInfo {
	out := make(map[string]EnvInfo, len(envs))
	for _, e := range envs {
		project := e.Spec.Project
		svc := strings.TrimPrefix(e.Spec.Service, project+"-")
		env := e.Labels[kube.LabelEnv]
		if env == "" {
			env = strings.TrimPrefix(e.Name, e.Spec.Service+"-")
		}
		out[e.Namespace+"/"+e.Name] = EnvInfo{
			Project: project,
			Service: svc,
			Env:     env,
			Preview: IsPreviewEnv(e),
		}
	}
	return out
}

// IsPreviewEnv reports whether an env is a PR preview.
func IsPreviewEnv(e kube.KusoEnvironment) bool {
	return e.Spec.Kind == "preview" || e.Spec.PullRequest != nil || strings.HasPrefix(e.Labels[kube.LabelEnv], "preview-")
}

// Classify turns live pods into inventory rows: service (non-preview
// env pods), addon, or platform. Every other pod — previews, builds,
// crons, runs, any Job pod, tenant pods in other namespaces — is
// skipped. Init containers count: they run the same registry pulls.
func Classify(pods []corev1.Pod, envs map[string]EnvInfo) []db.VulnImage {
	var out []db.VulnImage
	seen := map[string]bool{}
	for i := range pods {
		p := &pods[i]
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		owner, ok := classifyPod(p, envs)
		if !ok {
			continue
		}
		owner.Namespace = p.Namespace
		owner.Workload = workloadName(p)
		if len(p.Spec.ImagePullSecrets) > 0 {
			owner.PullSecret = p.Namespace + "/" + p.Spec.ImagePullSecrets[0].Name
		}
		specImage := map[string]string{}
		for _, c := range append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...) {
			specImage[c.Name] = c.Image
		}
		for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
			digest := digestOf(cs.ImageID)
			if digest == "" {
				continue
			}
			ref := specImage[cs.Name]
			if ref == "" {
				ref = cs.Image
			}
			img := owner
			img.Digest = digest
			img.ImageRef = ImageRepo(ref) + "@" + digest
			key := strings.Join([]string{img.Digest, img.OwnerKind, img.Project, img.Service, img.Env, img.Addon, img.Namespace, img.Workload}, "\x00")
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, img)
		}
	}
	return out
}

func classifyPod(p *corev1.Pod, envs map[string]EnvInfo) (db.VulnImage, bool) {
	l := p.Labels
	if a := l[labelAddon]; a != "" {
		project := l[kube.LabelProject]
		return db.VulnImage{OwnerKind: db.VulnOwnerAddon, Project: project, Addon: strings.TrimPrefix(a, project+"-")}, true
	}
	if l["app.kubernetes.io/name"] == "kusoenvironment" {
		e, ok := envs[p.Namespace+"/"+l["app.kubernetes.io/instance"]]
		if !ok || e.Preview {
			return db.VulnImage{}, false
		}
		return db.VulnImage{OwnerKind: db.VulnOwnerService, Project: e.Project, Service: e.Service, Env: e.Env}, true
	}
	if l[kube.LabelProject] != "" || ownedByJob(p) {
		return db.VulnImage{}, false
	}
	if platformNamespaces[p.Namespace] || (p.Namespace == "kube-system" && isTraefik(l)) {
		return db.VulnImage{OwnerKind: db.VulnOwnerPlatform}, true
	}
	return db.VulnImage{}, false
}

func isTraefik(l map[string]string) bool {
	return l["app.kubernetes.io/name"] == "traefik" || l["app"] == "traefik"
}

func ownedByJob(p *corev1.Pod) bool {
	for _, o := range p.OwnerReferences {
		if o.Kind == "Job" {
			return true
		}
	}
	return false
}

// workloadName is the pod's top-level controller: the Deployment behind
// a ReplicaSet, a StatefulSet or DaemonSet, else the pod itself.
func workloadName(p *corev1.Pod) string {
	for _, o := range p.OwnerReferences {
		if o.Controller == nil || !*o.Controller {
			continue
		}
		if o.Kind == "ReplicaSet" {
			if h := p.Labels["pod-template-hash"]; h != "" {
				return strings.TrimSuffix(o.Name, "-"+h)
			}
		}
		return o.Name
	}
	return p.Name
}

// digestOf extracts the sha256 digest from a containerStatus imageID
// ("docker-pullable://repo@sha256:…", "repo@sha256:…" or "sha256:…").
func digestOf(imageID string) string {
	if i := strings.LastIndexByte(imageID, '@'); i >= 0 {
		imageID = imageID[i+1:]
	}
	if strings.HasPrefix(imageID, "sha256:") && len(imageID) == len("sha256:")+64 {
		return imageID
	}
	return ""
}

// Cluster is the live state inventory reads.
type Cluster interface {
	Pods(ctx context.Context) ([]corev1.Pod, error)
	Environments(ctx context.Context) ([]kube.KusoEnvironment, error)
}

// KubeCluster reads pods from the shared informer when it's warm.
type KubeCluster struct{ Kube *kube.Client }

func (c KubeCluster) Pods(ctx context.Context) ([]corev1.Pod, error) {
	if cached, ok := c.Kube.Cache.ListPodsByLabel(labels.Everything()); ok {
		out := make([]corev1.Pod, 0, len(cached))
		for _, p := range cached {
			out = append(out, *p)
		}
		return out, nil
	}
	list, err := c.Kube.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	return list.Items, nil
}

func (c KubeCluster) Environments(ctx context.Context) ([]kube.KusoEnvironment, error) {
	envs, err := c.Kube.ListKusoEnvironments(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	return envs, nil
}

// Inventory classifies every live pod.
func Inventory(ctx context.Context, c Cluster) ([]db.VulnImage, error) {
	envs, err := c.Environments(ctx)
	if err != nil {
		return nil, err
	}
	pods, err := c.Pods(ctx)
	if err != nil {
		return nil, err
	}
	return Classify(pods, EnvIndex(envs)), nil
}
