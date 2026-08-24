package builds

import (
	"context"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// envKeyRE is the standard POSIX env-var identifier. Build env keys are
// rendered into an `ENV <key> <value>` line in the build job's shell, so a key
// with shell metacharacters ($(...), ;, spaces) would be a command-injection
// vector. Only valid identifiers may be injected — anything else is dropped.
var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedBuildEnvKeys are names that must never be injected as build-time
// env. Two classes:
//
//  1. kuso/kubelet-managed container bookkeeping (PORT desyncs from
//     spec.port; HOME/PATH/etc. are shell/container internals).
//
//  2. RUNTIME-ONLY environment selectors — chiefly NODE_ENV. A user (often
//     via a Coolify migration) sets NODE_ENV=production as a service env
//     var; that's correct at RUNTIME, but injecting it into the BUILD makes
//     npm/pnpm/yarn skip devDependencies, so any build needing a devDep (a
//     husky prepare hook, typescript, the bundler itself) fails with
//     "<tool>: not found". The build step's own tooling (next build / vite
//     build) sets NODE_ENV=production itself when it needs a production
//     bundle, so dropping it here is safe and matches how nixpacks/Heroku
//     behave. RAILS_ENV is the Ruby analogue; CI/DEBUG/NEXT_RUNTIME/
//     VERCEL_ENV similarly steer build behaviour in ways the user's runtime
//     value should not dictate at build time.
//
// This list MUST mirror the build job's own RESERVED list in
// buildcontroller/render.go (the script filters EXTRA_ENVS against it). The
// two had diverged — render.go listed NODE_ENV but this map didn't — which
// is exactly what let NODE_ENV=production reach the build and break installs.
// Keep them in lockstep.
var reservedBuildEnvKeys = map[string]bool{
	// Container / shell bookkeeping (kubelet/kuso-managed).
	"PORT": true, "HOSTNAME": true, "HOME": true, "PATH": true,
	"PWD": true, "USER": true, "SHELL": true, "TERM": true,
	"LANG": true, "LC_ALL": true, "LC_CTYPE": true,
	"NODE_OPTIONS": true, "NODE_VERSION": true, "NPM_CONFIG_LOGLEVEL": true,
	"DEBIAN_FRONTEND": true,
	// Runtime-only environment selectors — must not steer the build.
	"NODE_ENV": true, "DEBUG": true, "CI": true,
	"VERCEL_ENV": true, "NEXT_RUNTIME": true, "RAILS_ENV": true,
}

// ---- secret-ref build-env values -----------------------------------------
//
// SECURITY: spec.buildEnv used to carry secretKeyRef-sourced values RESOLVED
// TO PLAINTEXT (addon DATABASE_URLs, passwords). Those literals then
// persisted into the PUBLISHED image: as `--opt build-arg:` values
// (recoverable from the pushed image's config/history) and, for nixpacks, as
// `ENV` lines in the generated Dockerfile (a permanent layer). Anyone with
// registry read access could pull an image and recover live credentials.
//
// Secret-sourced vars are therefore stamped as a REFERENCE
// (kuso-secret-ref://<secret>/<key>), never plaintext. The buildcontroller
// resolves the ref into a kubelet secretKeyRef env mount on the build pod
// and — for user-authored Dockerfiles — exposes it as a buildkit secret the
// Dockerfile opts into via `RUN --mount=type=secret,id=<KEY>`, which never
// persists in a layer. User-authored literal values keep flowing as build
// args / baked ENV: the user wrote them as build-time config
// (NEXT_PUBLIC_*), they are not kuso-managed secrets.
const buildEnvSecretRefPrefix = "kuso-secret-ref://"

// Charset guards for the ref's two segments. Secret names are DNS-1123
// subdomains (no '/'), secret keys are [-._a-zA-Z0-9] (no '/'), so
// "<secret>/<key>" splits unambiguously on the first '/'. A ref that fails
// these is dropped rather than passed through — a ref-shaped value must
// never degrade into a literal build arg.
var (
	secretRefNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	secretRefKeyRE  = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)
)

// BuildEnvSecretRef formats the spec.buildEnv reference for a secret-sourced
// var. Returns "" when either segment fails its charset (callers skip the
// entry, matching the omit-on-unresolvable behaviour).
func BuildEnvSecretRef(secret, key string) string {
	if !secretRefNameRE.MatchString(secret) || !secretRefKeyRE.MatchString(key) {
		return ""
	}
	return buildEnvSecretRefPrefix + secret + "/" + key
}

// ParseBuildEnvSecretRef inverts BuildEnvSecretRef. ok=false for anything
// that isn't a well-formed ref — including prefix-carrying junk, which
// callers must DROP (not treat as a literal).
func ParseBuildEnvSecretRef(v string) (secret, key string, ok bool) {
	rest, found := strings.CutPrefix(v, buildEnvSecretRefPrefix)
	if !found {
		return "", "", false
	}
	secret, key, found = strings.Cut(rest, "/")
	if !found || !secretRefNameRE.MatchString(secret) || !secretRefKeyRE.MatchString(key) {
		return "", "", false
	}
	return secret, key, true
}

// IsBuildEnvSecretRef reports whether a spec.buildEnv value is (or claims to
// be) a secret reference rather than a user-authored literal.
func IsBuildEnvSecretRef(v string) bool {
	return strings.HasPrefix(v, buildEnvSecretRefPrefix)
}

// ---- sensitive literal detection -----------------------------------------
//
// SECURITY: the kuso-secret-ref:// machinery above protects secretKeyRef-
// sourced vars (addon conn strings), but it classifies on TRANSPORT, not on
// sensitivity. A user who types a credential straight into the value field —
// `kuso env set svc OPENAI_API_KEY=sk-...` — produces a LITERAL, and literals
// are baked into the published image. That is how a live OpenAI key, two
// OAuth client secrets, a webhook secret and an R2 access key ended up as
// plaintext `ENV` lines in a shipped image layer.
//
// There is no way to tell a secret from a config value by transport alone, so
// we fall back to the key NAME. This is a heuristic: it cannot be exact, and
// it deliberately errs toward withholding. A withheld var that the build
// actually needed produces a build-time failure the user can see and fix (move
// it to buildArgs); a baked secret is silent and permanent. Prefer the loud
// failure.
//
// NEXT_PUBLIC_* is the explicit carve-out: the Next.js convention is that
// these are inlined into the CLIENT bundle at build time, so they are public
// by definition AND required at build time. Withholding them would break every
// Next.js build for no security gain — a NEXT_PUBLIC_ value is already served
// to every browser.
// A bare "*_KEY" suffix is deliberately NOT matched: GOOD_KEY, SORT_KEY,
// CACHE_KEY and IDEMPOTENCY_KEY are ordinary config, and withholding them
// would break builds for no gain. Only KEY preceded by a credential-ish
// qualifier (API/ACCESS/SECRET/PRIVATE/SIGNING/ENCRYPTION/…) counts.
var sensitiveBuildEnvRE = regexp.MustCompile(
	`(?i)(SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL|` +
		`(API|APP|ACCESS|PRIVATE|PUBLISHABLE|SIGNING|ENCRYPTION|MASTER|CLIENT|SERVICE)_?KEY|` +
		`APIKEY|^KEY$|DSN|SALT|CERT|SIGNING|AUTH$)`,
)

// IsSensitiveBuildEnvKey reports whether a literal env-var KEY looks like a
// credential and must therefore never be injected into a build (where it would
// persist in the published image). Exported so the HTTP/CLI layers can warn at
// write time using exactly the same rule the build path enforces.
//
// NEXT_PUBLIC_-prefixed keys are always build-safe: Next.js inlines them into
// the client bundle, so they are public by construction.
func IsSensitiveBuildEnvKey(name string) bool {
	if strings.HasPrefix(name, "NEXT_PUBLIC_") {
		return false
	}
	return sensitiveBuildEnvRE.MatchString(name)
}

// secretLookup returns the literal value of a secret key, or false if absent.
type secretLookup func(secret, key string) (string, bool)

// buildEnvFromVars resolves a service's env vars into build-time KEY=VALUE
// entries. Literal Values pass through; secretKeyRef vars become
// kuso-secret-ref:// references (lookup only proves the secret exists — the
// plaintext value never leaves this function, see buildEnvSecretRefPrefix);
// unresolvable refs and reserved keys are omitted. Pure (lookup injected) so
// it's unit-testable without kube.
func buildEnvFromVars(vars []kube.KusoEnvVar, lookup secretLookup) map[string]string {
	out := map[string]string{}
	for _, v := range vars {
		if v.Name == "" || reservedBuildEnvKeys[v.Name] || !envKeyRE.MatchString(v.Name) {
			continue
		}
		if v.Value != "" {
			// A user-typed LITERAL whose key looks like a credential is
			// withheld from the build: literals bake into the published
			// image (nixpacks ENV layer / docker build-arg history), so a
			// secret set as a plain value would persist there forever. The
			// secretKeyRef path below is the only channel that can carry a
			// secret into a build safely. Runtime pods are unaffected —
			// they read the value from the deployment's envFrom.
			if IsSensitiveBuildEnvKey(v.Name) {
				continue
			}
			out[v.Name] = v.Value
			continue
		}
		// secretKeyRef: valueFrom.secretKeyRef.{name,key}
		secret, key := secretKeyRefOf(v.ValueFrom)
		if secret == "" || key == "" {
			continue
		}
		if _, ok := lookup(secret, key); ok {
			if ref := BuildEnvSecretRef(secret, key); ref != "" {
				out[v.Name] = ref
			}
		}
		// unresolvable → omit (addon conn secret may not exist yet)
	}
	return out
}

// secretKeyRefOf pulls {name,key} out of a valueFrom map's secretKeyRef.
func secretKeyRefOf(valueFrom map[string]any) (name, key string) {
	if valueFrom == nil {
		return "", ""
	}
	ref, ok := valueFrom["secretKeyRef"].(map[string]any)
	if !ok {
		return "", ""
	}
	name, _ = ref["name"].(string)
	key, _ = ref["key"].(string)
	return name, key
}

// resolveBuildEnv gathers a service's effective env as build-time entries:
// literals verbatim, secret-sourced vars as kuso-secret-ref:// references
// (the cluster read only proves the secret+key exist — plaintext is never
// stamped onto the CR). Used at build-trigger to populate
// KusoBuild.spec.buildEnv. A missing/unreadable secret just omits that var
// (logged by the caller) — never fatal.
func (s *Service) resolveBuildEnv(ctx context.Context, ns string, vars []kube.KusoEnvVar) map[string]string {
	cache := map[string]map[string][]byte{}
	lookup := func(secret, key string) (string, bool) {
		data, seen := cache[secret]
		if !seen {
			sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, secret, metav1.GetOptions{})
			if err != nil {
				cache[secret] = nil
				return "", false
			}
			data = sec.Data
			cache[secret] = data
		}
		if data == nil {
			return "", false
		}
		b, ok := data[key]
		return string(b), ok
	}
	return buildEnvFromVars(vars, lookup)
}
