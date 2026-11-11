package addons

import (
	"slices"
	"strings"

	"kuso/server/internal/kube"
)

// SharedWithPreviews reports whether a project addon may reach a preview env
// that has no clone of it. Native s3 is shared read-only: the preview mounts
// PreviewReadOnlyConn in place of the read-write conn (BLOB-2). Mailpit is a
// dev mail catcher holding no customer data. Everything else, external s3
// included, would hand PR code a live production datastore; an external
// addon reaches a preview only through an explicit previewEnvVars ref.
func SharedWithPreviews(a *kube.KusoAddon) bool {
	switch a.Spec.Kind {
	case "s3":
		return !isExternal(a)
	case "mailpit":
		return true
	}
	return false
}

// PreviewReadOnlyConn is the Secret a preview mounts instead of a native s3
// addon's production conn: the kusoaddon chart's read-only bucket user, with
// the same key names. "" for every other addon.
func PreviewReadOnlyConn(a *kube.KusoAddon) string {
	if a.Spec.Kind != "s3" || isExternal(a) {
		return ""
	}
	return a.Name + "-readonly"
}

func isExternal(a *kube.KusoAddon) bool {
	return a.Spec.External != nil && a.Spec.External.SecretName != ""
}

// cloneableForNamedEnvs reports whether a named env (staging/qa) could have
// its own copy of the addon: env groups and EnsureEnvAddons clone native and
// instance-shared postgres/redis. External addons are always shared with
// named envs (env groups refuse to clone them), so they are not withheld.
func cloneableForNamedEnvs(a *kube.KusoAddon) bool {
	if isExternal(a) {
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
//     dropped, including ones the preview already mounts; a native s3 conn
//     is replaced by its PreviewReadOnlyConn.
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
		case preview && PreviewReadOnlyConn(a) != "":
			if ro := PreviewReadOnlyConn(a); !slices.Contains(kept, ro) {
				kept = append(kept, ro)
			}
			continue
		case !preview && cloneableForNamedEnvs(a) && !slices.Contains(prev, n):
			withheld = append(withheld, n)
			continue
		}
		kept = append(kept, n)
	}
	return kept, withheld
}

// AnnotationSharedS3ReadOnly marks a named env (staging/qa) that shares the
// project's s3 addons instead of owning copies: it mounts each native
// bucket through the read-only user, like a preview does (BLOB-R9). Only
// envs created since are stamped; older envs keep their read-write mount,
// and `--s3-read-write` at creation opts a new one out.
const AnnotationSharedS3ReadOnly = "kuso.sislelabs.com/shared-s3-readonly"

// SharedS3ReadOnly reports whether the env carries AnnotationSharedS3ReadOnly.
func SharedS3ReadOnly(annotations map[string]string) bool {
	return annotations[AnnotationSharedS3ReadOnly] == "true"
}

// ReadOnlyS3Swaps maps each native project s3 addon's conn Secret to its
// read-only Secret. Env-scoped clones are skipped: an env's own copy is
// its own to write.
func ReadOnlyS3Swaps(all []kube.KusoAddon) map[string]string {
	out := map[string]string{}
	for i := range all {
		a := &all[i]
		if a.Labels[kube.LabelEnv] != "" {
			continue
		}
		if ro := PreviewReadOnlyConn(a); ro != "" {
			out[connSecretName(a.Name)] = ro
		}
	}
	return out
}

// SwapToReadOnlyS3 replaces every conn in list that swaps names with its
// read-only Secret. A read-only Secret already in list stays only while
// allowed(its read-write conn) holds, so unsubscribing still unmounts it.
func SwapToReadOnlyS3(list []string, swaps map[string]string, allowed func(conn string) bool) []string {
	if len(swaps) == 0 {
		return list
	}
	rwOf := make(map[string]string, len(swaps))
	for rw, ro := range swaps {
		rwOf[ro] = rw
	}
	out := make([]string, 0, len(list))
	add := func(n string) {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	for _, n := range list {
		if ro, ok := swaps[n]; ok {
			add(ro)
			continue
		}
		if rw, ok := rwOf[n]; ok && allowed != nil && !allowed(rw) {
			continue
		}
		add(n)
	}
	return out
}

// SubscriptionAllows reports whether an env with subscribedAddons (nil =
// legacy mount-all) may mount conn.
func SubscriptionAllows(subscribedAddons []string, project string) func(conn string) bool {
	return func(conn string) bool {
		return subscribedAddons == nil || subscribesToConn(subscribedAddons, project, conn)
	}
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
