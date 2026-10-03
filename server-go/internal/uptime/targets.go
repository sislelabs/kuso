package uptime

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"kuso/server/internal/health"
	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"
)

// Pause reasons.
const (
	PausedStopped      = "stopped"
	PausedAsleep       = "asleep"
	PausedNoImage      = "no-image"
	PausedScaledToZero = "scaled-to-zero"
	PausedNoDeployment = "no-deployment"
)

// Target is one production web environment and how the loop should
// treat it this tick.
type Target struct {
	Namespace string
	Env       string
	Project   string
	Service   string // short name
	URL       string
	// Disabled: the project or service opted out. Not pinged, no row.
	Disabled bool
	// Paused: not serving on purpose ("" = serving).
	Paused string
	// PodsBad: pods are crash-looping or can't pull their image.
	PodsBad bool
}

func (t Target) Key() string { return t.Namespace + "/" + t.Env }

// Workload is what BuildTargets needs to know about an env's Deployment.
type Workload struct {
	Exists   bool
	Replicas int32
}

// BuildTargets picks the envs uptime checks cover: production, web
// (not worker, not internal). workload and podsBad are keyed by
// "<namespace>/<env>".
func BuildTargets(
	projects []kube.KusoProject, services []kube.KusoService, envs []kube.KusoEnvironment,
	workload map[string]Workload, podsBad map[string]bool,
) []Target {
	projByName := make(map[string]*kube.KusoProject, len(projects))
	for i := range projects {
		projByName[projects[i].Name] = &projects[i]
	}
	svcByKey := make(map[string]*kube.KusoService, len(services))
	for i := range services {
		svcByKey[services[i].Namespace+"/"+services[i].Name] = &services[i]
	}
	var out []Target
	for i := range envs {
		env := &envs[i]
		svc, ok := svcByKey[env.Namespace+"/"+env.Spec.Service]
		if !ok {
			continue // orphan env
		}
		if !scaledown.IsProductionEnv(svc, env) {
			continue
		}
		if env.Spec.Runtime == "worker" || svc.Spec.Runtime == "worker" || env.Spec.Internal || svc.Spec.Internal {
			continue
		}
		project := env.Spec.Project
		if project == "" {
			project = env.Labels[kube.LabelProject]
		}
		t := Target{
			Namespace: env.Namespace,
			Env:       env.Name,
			Project:   project,
			Service:   strings.TrimPrefix(env.Spec.Service, project+"-"),
		}
		path := ""
		if svc.Spec.Uptime != nil {
			t.Disabled = svc.Spec.Uptime.Disabled
			path = svc.Spec.Uptime.Path
		}
		if p := projByName[project]; p != nil && p.Spec.Uptime != nil && p.Spec.Uptime.Disabled {
			t.Disabled = true
		}
		if path == "" && env.Spec.Healthcheck != nil {
			path = env.Spec.Healthcheck.Path
		}
		t.URL = TargetURL(t.Namespace, t.Env, path)

		w := workload[t.Key()]
		switch {
		case env.Spec.Stopped || svc.Spec.Stopped:
			t.Paused = PausedStopped
		case !w.Exists:
			t.Paused = PausedNoDeployment
		case w.Replicas == 0 && env.Annotations[scaledown.PreSleepReplicasAnnotation] != "":
			t.Paused = PausedAsleep
		case w.Replicas == 0 && env.Spec.Image == nil:
			t.Paused = PausedNoImage
		case w.Replicas == 0:
			t.Paused = PausedScaledToZero
		}
		t.PodsBad = podsBad[t.Key()]
		out = append(out, t)
	}
	return out
}

// Cluster reads the current targets.
type Cluster interface {
	Targets(ctx context.Context) ([]Target, error)
}

// KubeCluster is the live Cluster.
type KubeCluster struct {
	Kube *kube.Client
}

func (k KubeCluster) Targets(ctx context.Context) ([]Target, error) {
	// Empty ns → cluster-wide. A failed list must read as "unknown", not
	// "nothing to check": the caller prunes rows for missing targets.
	projects, err := k.Kube.ListKusoProjects(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	services, err := k.Kube.ListKusoServices(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	envs, err := k.Kube.ListKusoEnvironments(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	pods, err := k.pods(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	podsBad := map[string]bool{}
	for _, p := range pods {
		if health.PodBadReason(p) != "" {
			podsBad[p.Namespace+"/"+p.Labels["app.kubernetes.io/instance"]] = true
		}
	}
	workload := make(map[string]Workload, len(envs))
	for i := range envs {
		w, err := k.workload(ctx, envs[i].Namespace, envs[i].Name)
		if err != nil {
			return nil, fmt.Errorf("get deployment %s/%s: %w", envs[i].Namespace, envs[i].Name, err)
		}
		workload[envs[i].Namespace+"/"+envs[i].Name] = w
	}
	return BuildTargets(projects, services, envs, workload, podsBad), nil
}

func (k KubeCluster) pods(ctx context.Context) ([]*corev1.Pod, error) {
	sel, err := labels.Parse(kube.LabelProject)
	if err != nil {
		return nil, err
	}
	if pods, ok := k.Kube.Cache.ListPodsByLabel(sel); ok {
		return pods, nil
	}
	list, err := k.Kube.Clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: kube.LabelProject})
	if err != nil {
		return nil, err
	}
	out := make([]*corev1.Pod, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, nil
}

func (k KubeCluster) workload(ctx context.Context, ns, name string) (Workload, error) {
	dep, ok := k.Kube.Cache.GetDeployment(ns, name)
	if !ok {
		d, err := k.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return Workload{}, nil
		}
		if err != nil {
			return Workload{}, err
		}
		dep = d
	}
	if dep.Spec.Replicas == nil {
		return Workload{Exists: true, Replicas: 1}, nil
	}
	return Workload{Exists: true, Replicas: *dep.Spec.Replicas}, nil
}
