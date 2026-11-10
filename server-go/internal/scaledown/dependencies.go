package scaledown

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"kuso/server/internal/kube"
)

// In-cluster calls (`${{ api.URL }}` resolves to the target env's own
// Service DNS name) bypass both traefik and the activator, so neither the
// request counter nor the last-activity stamp sees them, and a slept
// target's Service has no endpoints and nothing on that path wakes it.
//
// kuso can't observe pod-to-pod traffic, so sleep follows the reference
// graph instead: an env that another env's envVars point at stays awake
// while any referrer is awake and not itself going to sleep this tick,
// and waking a referrer through the activator wakes what it references.
// Referrers and targets that are all idle still sleep together.

// serviceHostRE matches in-cluster Service DNS names,
// <service>.<namespace>.svc[.cluster.local], the shape ExpandServiceKey
// emits for HOST / URL / INTERNAL_URL.
var serviceHostRE = regexp.MustCompile(`([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)\.([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)\.svc(?:\.cluster\.local)?\b`)

// maxDependencyDepth bounds the wake cascade (web → api → auth → …).
const maxDependencyDepth = 4

// referencedEnvs returns the ns/name keys of every in-cluster Service the
// env's literal envVars point at, excluding itself. The env kuso Service
// is named after its env CR, so the key doubles as an env key.
func referencedEnvs(env *kube.KusoEnvironment) []string {
	self := env.Namespace + "/" + env.Name
	var out []string
	seen := map[string]bool{}
	for _, v := range env.Spec.EnvVars {
		if v.ValueFrom != nil || v.Value == "" {
			continue
		}
		for _, m := range serviceHostRE.FindAllStringSubmatch(v.Value, -1) {
			key := m[2] + "/" + m[1]
			if key == self || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// referrersByTarget inverts referencedEnvs over every env: target key →
// keys of the envs that call it.
func referrersByTarget(envs []kube.KusoEnvironment) map[string][]string {
	out := map[string][]string{}
	for i := range envs {
		e := &envs[i]
		from := e.Namespace + "/" + e.Name
		for _, to := range referencedEnvs(e) {
			out[to] = append(out[to], from)
		}
	}
	return out
}

// WakeDependencies wakes every env that ns/name references in-cluster and
// that scaledown put to sleep, transitively. Called by the activator when
// it wakes an env: the woken app's first server-side call to a slept
// sibling would otherwise be refused until the next scaledown tick.
// Only envs carrying the pre-sleep annotation are touched, so an env that
// was never slept, is awake, or is hard-stopped is left alone.
// Best-effort: failures are logged.
func WakeDependencies(ctx context.Context, kc *kube.Client, logger *slog.Logger, ns, name string, now time.Time) {
	if kc == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	visited := map[string]bool{ns + "/" + name: true}
	frontier := []string{ns + "/" + name}
	for depth := 0; depth < maxDependencyDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, key := range frontier {
			fns, fname := splitKey(key)
			env, err := kc.GetKusoEnvironment(ctx, fns, fname)
			if err != nil || env == nil {
				continue
			}
			for _, dep := range referencedEnvs(env) {
				if visited[dep] {
					continue
				}
				visited[dep] = true
				dns, dname := splitKey(dep)
				target, err := kc.GetKusoEnvironment(ctx, dns, dname)
				if err != nil || target == nil {
					continue
				}
				if target.Spec.Stopped || target.Annotations[PreSleepReplicasAnnotation] == "" {
					continue
				}
				if err := Wake(ctx, kc, logger, dns, dname, now); err != nil {
					logger.Warn("scaledown: wake dependency", "referrer", key, "env", dep, "err", err)
					continue
				}
				logger.Info("scaledown: woke dependency of a waking env", "referrer", key, "env", dep)
				next = append(next, dep)
			}
		}
		frontier = next
	}
}

func splitKey(key string) (ns, name string) {
	ns, name, _ = strings.Cut(key, "/")
	return ns, name
}
