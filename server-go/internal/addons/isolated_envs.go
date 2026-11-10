package addons

import (
	"slices"
	"strings"

	"kuso/server/internal/kube"
)

// SharedWithPreviews reports whether a project addon's PRODUCTION conn may be
// mounted on a preview env that has no clone of it. Only s3 (the preview
// follows its staging base, which shares the bucket unless it has its own
// clone; the dispatcher swaps in staging's clone when there is one) and
// mailpit (a dev mail catcher holding no customer data) are shared. Everything else —
// external databases, mongodb, mysql, brokers, valkey, clickhouse, … — would
// hand PR code a live production datastore.
func SharedWithPreviews(a *kube.KusoAddon) bool {
	switch a.Spec.Kind {
	case "s3", "mailpit":
		return true
	}
	return false
}

// cloneableForNamedEnvs reports whether a named env (staging/qa) could have
// its own copy of the addon: env groups and EnsureEnvAddons clone native and
// instance-shared postgres/redis. External addons are always shared with
// named envs (env groups refuse to clone them), so they are not withheld.
func cloneableForNamedEnvs(a *kube.KusoAddon) bool {
	if a.Spec.External != nil && a.Spec.External.SecretName != "" {
		return false
	}
	return a.Spec.Kind == "postgres" || a.Spec.Kind == "redis"
}

// WithholdProductionConns removes PRODUCTION addon conns that must not reach
// an env isolated from production data, returning the kept list and the
// withheld names. It runs after the clone swap, so a project conn still in
// next has no clone for this env.
//
//   - preview envs: every project addon conn not SharedWithPreviews is
//     dropped, including ones the preview already mounts.
//   - named envs that own at least one clone (an env group or an isolated
//     staging/qa env): a cloneable-kind conn is never ADDED. One the env
//     already mounts is kept — env groups share addons with policy "shared"
//     on purpose, and that choice was made when the group was created.
//   - production and named envs without clones (which share production by
//     design) are untouched.
//
// prev is the env's envFromSecrets before this write; all is every addon of
// the project, clones included.
func WithholdProductionConns(next, prev []string, all []kube.KusoAddon, envScope string, preview bool) (kept, withheld []string) {
	if !preview && (envScope == "" || envScope == "production") {
		return next, nil
	}
	byConn := make(map[string]*kube.KusoAddon, len(all))
	isolated := preview
	for i := range all {
		a := &all[i]
		if env := a.Labels[kube.LabelEnv]; env != "" {
			if env == envScope {
				isolated = true
			}
			continue
		}
		byConn[connSecretName(a.Name)] = a
	}
	if !isolated {
		return next, nil
	}
	kept = make([]string, 0, len(next))
	for _, n := range next {
		a, ok := byConn[n]
		switch {
		case !ok:
		case preview && !SharedWithPreviews(a):
			withheld = append(withheld, n)
			continue
		case !preview && cloneableForNamedEnvs(a) && !slices.Contains(prev, n):
			withheld = append(withheld, n)
			continue
		}
		kept = append(kept, n)
	}
	return kept, withheld
}

// ScrubPreviewSharedMounts removes the project- and instance-shared Secrets
// from a preview's envFrom list. Mounted whole they hand PR code every
// shared key, including the instance-PG admin DSN (ISO-2).
func ScrubPreviewSharedMounts(list []string, project string) []string {
	shared := kube.SharedSecretNames(project)
	return slices.DeleteFunc(slices.Clone(list), func(n string) bool { return slices.Contains(shared, n) })
}

// DropPreviewSharedRefs removes a preview's env vars that read a shared
// Secret through secretKeyRef (the per-key sharedEnvKeys subscription it
// would otherwise inherit, ISO-2). A var the service lists in its
// previews.previewEnvVars as a shared-secret ref is the explicit opt-in and
// stays.
func DropPreviewSharedRefs(vars []kube.KusoEnvVar, project string, optIn []kube.KusoEnvVar) []kube.KusoEnvVar {
	shared := kube.SharedSecretNames(project)
	refName := func(v kube.KusoEnvVar) string {
		ref, _ := v.ValueFrom["secretKeyRef"].(map[string]any)
		name, _ := ref["name"].(string)
		return name
	}
	allowed := map[string]bool{}
	for _, v := range optIn {
		if slices.Contains(shared, refName(v)) {
			allowed[v.Name] = true
		}
	}
	return slices.DeleteFunc(slices.Clone(vars), func(v kube.KusoEnvVar) bool {
		return slices.Contains(shared, refName(v)) && !allowed[v.Name]
	})
}

// IsPreviewEnv reports whether an env CR is a PR preview.
func IsPreviewEnv(env *kube.KusoEnvironment) bool {
	return env.Spec.Kind == "preview" || env.Spec.PullRequest != nil ||
		strings.HasPrefix(env.Labels[kube.LabelEnv], "preview-pr-")
}
