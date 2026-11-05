package vulnscan

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// Live answers "what image does this run right now" for the API: from
// the pods when there are any, else the last inventory sighting (a
// sleeping or stopped workload), else — for an env never inventoried —
// the env's configured image on the in-cluster registry.
type Live struct {
	Kube    *kube.Client
	DB      *db.DB
	Digests DigestResolver
	Now     func() time.Time
}

func (l Live) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l Live) pods(ctx context.Context, ns string, sel map[string]string) ([]corev1.Pod, error) {
	selector := labels.SelectorFromSet(sel)
	if cached, ok := l.Kube.Cache.ListPodsByLabel(selector); ok {
		var out []corev1.Pod
		for _, p := range cached {
			if p.Namespace == ns {
				out = append(out, *p)
			}
		}
		return out, nil
	}
	list, err := l.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	return list.Items, nil
}

// primaryImage is the first container of the first live pod, the
// workload's own image (sidecars and init containers are reported by
// the inventory, not here).
func primaryImage(pods []corev1.Pod, owner db.VulnImage) *db.VulnImage {
	for i := range pods {
		p := &pods[i]
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed || len(p.Spec.Containers) == 0 {
			continue
		}
		main := p.Spec.Containers[0]
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name != main.Name {
				continue
			}
			d := digestOf(cs.ImageID)
			if d == "" {
				break
			}
			im := owner
			im.Digest, im.ImageRef = d, ImageRepo(main.Image)+"@"+d
			im.Namespace = p.Namespace
			if im.Workload == "" {
				im.Workload = workloadName(p)
			}
			if len(p.Spec.ImagePullSecrets) > 0 {
				im.PullSecret = p.Namespace + "/" + p.Spec.ImagePullSecrets[0].Name
			}
			return &im
		}
	}
	return nil
}

func (l Live) lastSeen(ctx context.Context, f db.VulnImageFilter) (*db.VulnImage, error) {
	f.SeenSince = l.now().Add(-GoneAfter)
	rows, err := l.DB.ListVulnImages(ctx, f)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil // newest sighting first
}

// ServiceImage returns the image a service env runs, or nil.
func (l Live) ServiceImage(ctx context.Context, project, service, env string) (*db.VulnImage, error) {
	envs, err := l.Kube.ListKusoEnvironmentsByLabels(ctx, "", map[string]string{
		kube.LabelProject: project, kube.LabelService: service, kube.LabelEnv: env,
	})
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	var e *kube.KusoEnvironment
	for i := range envs {
		if envs[i].Spec.Project == project && envs[i].Spec.Service == project+"-"+service {
			e = &envs[i]
			break
		}
	}
	owner := db.VulnImage{OwnerKind: db.VulnOwnerService, Project: project, Service: service, Env: env}
	if e != nil {
		owner.Workload = e.Name
		pods, err := l.pods(ctx, e.Namespace, map[string]string{
			"app.kubernetes.io/name": "kusoenvironment", "app.kubernetes.io/instance": e.Name,
		})
		if err != nil {
			return nil, err
		}
		if im := primaryImage(pods, owner); im != nil {
			return im, nil
		}
	}
	if im, err := l.lastSeen(ctx, db.VulnImageFilter{OwnerKind: db.VulnOwnerService, Project: project, Service: service, Env: env}); im != nil || err != nil {
		return im, err
	}
	if e == nil || e.Spec.Image == nil || e.Spec.Image.Repository == "" || e.Spec.Image.Tag == "" || l.Digests == nil {
		return nil, nil
	}
	ref := e.Spec.Image.Repository + ":" + e.Spec.Image.Tag
	d, err := l.Digests.ResolveDigest(ctx, ref)
	if err != nil || d == "" {
		return nil, nil // can't pin a digest: nothing scannable to report
	}
	owner.Namespace, owner.Digest, owner.ImageRef = e.Namespace, d, e.Spec.Image.Repository+"@"+d
	return &owner, nil
}

// AddonImage returns the image an addon runs, or nil.
func (l Live) AddonImage(ctx context.Context, project string, a *kube.KusoAddon) (*db.VulnImage, error) {
	short := strings.TrimPrefix(a.Name, project+"-")
	owner := db.VulnImage{OwnerKind: db.VulnOwnerAddon, Project: project, Addon: short}
	pods, err := l.pods(ctx, a.Namespace, map[string]string{labelAddon: a.Name})
	if err != nil {
		return nil, err
	}
	if im := primaryImage(pods, owner); im != nil {
		return im, nil
	}
	return l.lastSeen(ctx, db.VulnImageFilter{OwnerKind: db.VulnOwnerAddon, Project: project, Addon: short})
}

// ProjectImages lists every service + addon image of a project: the
// live classification, plus the last sighting of owners with no pods
// right now.
func (l Live) ProjectImages(ctx context.Context, project string) ([]db.VulnImage, error) {
	inv, err := Inventory(ctx, projectCluster{Kube: l.Kube, Project: project})
	if err != nil {
		return nil, err
	}
	type ownerEnv struct{ kind, service, env, addon string }
	live := map[ownerEnv]bool{}
	var out []db.VulnImage
	now := l.now()
	for _, im := range inv {
		if im.Project != project || im.OwnerKind == db.VulnOwnerPlatform {
			continue
		}
		im.LastSeenAt = now
		live[ownerEnv{im.OwnerKind, im.Service, im.Env, im.Addon}] = true
		out = append(out, im)
	}
	stored, err := l.DB.ListVulnImages(ctx, db.VulnImageFilter{Project: project, SeenSince: now.Add(-GoneAfter)})
	if err != nil {
		return nil, err
	}
	for _, im := range LatestPerOwner(stored) {
		if im.OwnerKind != db.VulnOwnerPlatform && !live[ownerEnv{im.OwnerKind, im.Service, im.Env, im.Addon}] {
			out = append(out, im)
		}
	}
	return LatestPerOwner(out), nil
}

// projectCluster is one project's slice of the cluster, by the project
// label every env and addon pod (and env CR) carries. Cluster-wide, so a
// project in a custom namespace is found the same way.
type projectCluster struct {
	Kube    *kube.Client
	Project string
}

func (c projectCluster) Pods(ctx context.Context) ([]corev1.Pod, error) {
	sel := labels.SelectorFromSet(labels.Set{kube.LabelProject: c.Project})
	if cached, ok := c.Kube.Cache.ListPodsByLabel(sel); ok {
		out := make([]corev1.Pod, 0, len(cached))
		for _, p := range cached {
			out = append(out, *p)
		}
		return out, nil
	}
	list, err := c.Kube.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	return list.Items, nil
}

func (c projectCluster) Environments(ctx context.Context) ([]kube.KusoEnvironment, error) {
	envs, err := c.Kube.ListKusoEnvironmentsByLabels(ctx, "", map[string]string{kube.LabelProject: c.Project})
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	return envs, nil
}
