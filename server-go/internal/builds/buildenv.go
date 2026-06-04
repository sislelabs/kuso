package builds

import (
	"context"
	"regexp"

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

// secretLookup returns the literal value of a secret key, or false if absent.
type secretLookup func(secret, key string) (string, bool)

// buildEnvFromVars resolves a service's env vars into build-time KEY=VALUE
// literals. Literal Values pass through; secretKeyRef vars are resolved via
// lookup; unresolvable refs and reserved keys are omitted. Pure (lookup
// injected) so it's unit-testable without kube.
func buildEnvFromVars(vars []kube.KusoEnvVar, lookup secretLookup) map[string]string {
	out := map[string]string{}
	for _, v := range vars {
		if v.Name == "" || reservedBuildEnvKeys[v.Name] || !envKeyRE.MatchString(v.Name) {
			continue
		}
		if v.Value != "" {
			out[v.Name] = v.Value
			continue
		}
		// secretKeyRef: valueFrom.secretKeyRef.{name,key}
		secret, key := secretKeyRefOf(v.ValueFrom)
		if secret == "" || key == "" {
			continue
		}
		if val, ok := lookup(secret, key); ok {
			out[v.Name] = val
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

// resolveBuildEnv gathers a service's effective env as build-time literals,
// reading referenced secrets from the cluster. Used at build-trigger to
// populate KusoBuild.spec.buildEnv. A missing/unreadable secret just omits that
// var (logged by the caller) — never fatal.
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
