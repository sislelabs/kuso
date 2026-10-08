package uptime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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
	// RollingOut: pods are still coming up (deploy, wake, first start).
	RollingOut bool
	// Unknown: the workload couldn't be read this time. The target is
	// neither probed nor pruned; its row is left as it was.
	Unknown bool
}

func (t Target) Key() string { return t.Namespace + "/" + t.Env }

// Workload is what BuildTargets needs to know about an env's Deployment.
type Workload struct {
	Exists     bool
	Replicas   int32
	RollingOut bool
	// Unknown: the Deployment lookup failed.
	Unknown bool
}

// RolloutGrace is how long after a pod is created a not-yet-ready
// Deployment counts as starting up rather than down.
const RolloutGrace = 5 * time.Minute

func servicesByKey(services []kube.KusoService) map[string]*kube.KusoService {
	out := make(map[string]*kube.KusoService, len(services))
	for i := range services {
		out[services[i].Namespace+"/"+services[i].Name] = &services[i]
	}
	return out
}

// isTargetEnv: production, web (not worker, not internal).
func isTargetEnv(svcByKey map[string]*kube.KusoService, env *kube.KusoEnvironment) (*kube.KusoService, bool) {
	svc, ok := svcByKey[env.Namespace+"/"+env.Spec.Service]
	if !ok {
		return nil, false // orphan env
	}
	if !scaledown.IsProductionEnv(svc, env) {
		return nil, false
	}
	if env.Spec.Runtime == "worker" || svc.Spec.Runtime == "worker" || env.Spec.Internal || svc.Spec.Internal {
		return nil, false
	}
	return svc, true
}

func envProject(env *kube.KusoEnvironment) string {
	if env.Spec.Project != "" {
		return env.Spec.Project
	}
	return env.Labels[kube.LabelProject]
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
	svcByKey := servicesByKey(services)
	var out []Target
	for i := range envs {
		env := &envs[i]
		svc, ok := isTargetEnv(svcByKey, env)
		if !ok {
			continue
		}
		project := envProject(env)
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
		case w.Unknown:
			t.Unknown = true
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
		t.RollingOut = w.RollingOut
		out = append(out, t)
	}
	return out
}

// Cluster reads the current targets. project "" means every project.
type Cluster interface {
	Targets(ctx context.Context, project string) ([]Target, error)
}

// KubeCluster is the live Cluster.
type KubeCluster struct {
	Kube   *kube.Client
	Logger *slog.Logger
}

func (k KubeCluster) Targets(ctx context.Context, project string) ([]Target, error) {
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
	allEnvs, err := k.Kube.ListKusoEnvironments(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	// Only production web envs (of the asked-for project) need a
	// Deployment lookup; previews and workers would cost a live GET
	// each when they have no Deployment yet.
	svcByKey := servicesByKey(services)
	var envs []kube.KusoEnvironment
	for i := range allEnvs {
		if project != "" && envProject(&allEnvs[i]) != project {
			continue
		}
		if _, ok := isTargetEnv(svcByKey, &allEnvs[i]); ok {
			envs = append(envs, allEnvs[i])
		}
	}
	pods, err := k.pods(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	podsBad := map[string]bool{}
	newestPod := map[string]time.Time{}
	for _, p := range pods {
		key := p.Namespace + "/" + p.Labels["app.kubernetes.io/instance"]
		if health.PodBadReason(p) != "" {
			podsBad[key] = true
		}
		if c := p.CreationTimestamp.Time; c.After(newestPod[key]) {
			newestPod[key] = c
		}
	}
	now := time.Now()
	workload := make(map[string]Workload, len(envs))
	for i := range envs {
		key := envs[i].Namespace + "/" + envs[i].Name
		dep, err := k.deployment(ctx, envs[i].Namespace, envs[i].Name)
		if err != nil {
			// One bad lookup must not blind the whole tick: this target
			// is skipped (not pruned) and the rest are checked.
			if k.Logger != nil {
				k.Logger.Warn("uptime: get deployment", "env", key, "err", err)
			}
			workload[key] = Workload{Unknown: true}
			continue
		}
		workload[key] = WorkloadOf(dep, newestPod[key], now)
	}
	return BuildTargets(projects, services, envs, workload, podsBad), nil
}

func (k KubeCluster) pods(ctx context.Context, project string) ([]*corev1.Pod, error) {
	var sel labels.Selector
	if project != "" {
		sel = labels.SelectorFromSet(labels.Set{kube.LabelProject: project})
	} else {
		s, err := labels.Parse(kube.LabelProject)
		if err != nil {
			return nil, err
		}
		sel = s
	}
	if pods, ok := k.Kube.Cache.ListPodsByLabel(sel); ok {
		return pods, nil
	}
	list, err := k.Kube.Clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, err
	}
	out := make([]*corev1.Pod, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, nil
}

// deployment returns nil when the env has no Deployment.
func (k KubeCluster) deployment(ctx context.Context, ns, name string) (*appsv1.Deployment, error) {
	if dep, ok := k.Kube.Cache.GetDeployment(ns, name); ok {
		return dep, nil
	}
	d, err := k.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// WorkloadOf summarises a Deployment (nil = none) for BuildTargets.
// newestPod is the creation time of the env's youngest pod.
func WorkloadOf(dep *appsv1.Deployment, newestPod, now time.Time) Workload {
	if dep == nil {
		return Workload{}
	}
	w := Workload{Exists: true, Replicas: 1}
	if dep.Spec.Replicas != nil {
		w.Replicas = *dep.Spec.Replicas
	}
	if w.Replicas == 0 {
		return w
	}
	deadlineExceeded := false
	for _, c := range dep.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded" {
			deadlineExceeded = true
		}
	}
	// A rollout the controller still reports as in progress. Bounded by
	// progressDeadlineSeconds: past it the condition flips and failures
	// count again.
	rolling := !deadlineExceeded &&
		(dep.Generation > dep.Status.ObservedGeneration || dep.Status.UpdatedReplicas < w.Replicas)
	// A scale-up from zero (wake, first deploy) doesn't touch the
	// Progressing condition, so a young, not-yet-ready pod counts too.
	starting := dep.Status.ReadyReplicas < w.Replicas && !newestPod.IsZero() && now.Sub(newestPod) < RolloutGrace
	w.RollingOut = rolling || starting
	return w
}
