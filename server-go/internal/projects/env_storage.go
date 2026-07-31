package projects

import "kuso/server/internal/kube"

// EnvStorage is where a written env value should live. The user no longer
// picks "plain vs secret" — they just type a value, and the server decides
// storage so that (a) the plaintext stays off the KusoService CR whenever
// possible, while (b) values the BUILD needs remain resolvable at build
// time. This is the "one secret primitive" rule: to the user there is only
// "a value"; the three underlying forms are an implementation detail.
type EnvStorage int

const (
	// StorageSecret: store the value in the kuso-managed
	// <project>-<service>-secrets Secret (via secretValue). The default —
	// the plaintext never lands on the CR. Viewable via reveal, editable,
	// removable.
	StorageSecret EnvStorage = iota
	// StorageCREnv: store as a literal on spec.envVars. Used ONLY when the
	// value must be resolvable at BUILD time (publicEnv / buildArgs
	// members), because the build resolves build env from spec.envVars and
	// managed secrets are not on the CR. Keeping these as CR env is what
	// makes the consolidation zero-degradation for build-time env and
	// NEXT_PUBLIC_* sentinel baking.
	StorageCREnv
	// StorageRef: the value is a ${{ name.KEY }} reference — addon/shared
	// wiring. Not a user-authored secret; stored as a secretKeyRef on
	// spec.envVars by the ref-resolution path. Kept distinct so the write
	// path routes it through varref expansion, not managed-secret storage.
	StorageRef
)

// chooseEnvStorage decides where a written value belongs, given the value
// itself and the service's build-facing declarations. Pure and testable.
//
// Rules, in order:
//  1. A ${{ … }} reference → StorageRef (addon/shared wiring).
//  2. A name the build needs — declared in publicEnv or buildArgs → CREnv,
//     so it stays resolvable at build time. reservedBuildEnvKeys are
//     runtime-only selectors the build must NOT see, so they are NOT
//     build-relevant and fall through to secret storage.
//  3. Everything else → Secret (the default; off the CR).
func chooseEnvStorage(name, value string, svc *kube.KusoService) EnvStorage {
	if isVarRefValue(value) {
		return StorageRef
	}
	if buildRelevant(name, svc) {
		return StorageCREnv
	}
	return StorageSecret
}

// isVarRefValue reports whether value is a pure ${{ name.KEY }} reference.
// Thin wrapper over the varref parser so env_storage stays self-documenting.
func isVarRefValue(value string) bool {
	_, ok, _ := ParseVarRef(value)
	return ok
}

// reservedBuildEnvNames mirrors builds.reservedBuildEnvKeys — the names the
// build must NOT see (runtime-only selectors like NODE_ENV + container
// bookkeeping). Duplicated here (not imported) because importing the builds
// package from projects would risk a cycle; keep in lockstep with
// server-go/internal/builds/buildenv.go.
var reservedBuildEnvNames = map[string]bool{
	"PORT": true, "HOSTNAME": true, "HOME": true, "PATH": true,
	"PWD": true, "USER": true, "SHELL": true, "TERM": true,
	"LANG": true, "LC_ALL": true, "LC_CTYPE": true,
	"NODE_OPTIONS": true, "NODE_VERSION": true, "NPM_CONFIG_LOGLEVEL": true,
	"DEBIAN_FRONTEND": true,
	"NODE_ENV": true, "DEBUG": true, "CI": true,
	"VERCEL_ENV": true, "NEXT_RUNTIME": true, "RAILS_ENV": true,
}

func reservedBuildEnvName(name string) bool { return reservedBuildEnvNames[name] }

// buildRelevant reports whether name is one the build needs resolvable
// (declared in publicEnv or buildArgs), and therefore must stay as CR env.
// Runtime-only reserved selectors (NODE_ENV, …) are explicitly NOT
// build-relevant — the build must not see them.
func buildRelevant(name string, svc *kube.KusoService) bool {
	if svc == nil {
		return false
	}
	if reservedBuildEnvName(name) {
		return false
	}
	for _, p := range svc.Spec.PublicEnv {
		if p == name {
			return true
		}
	}
	if _, ok := svc.Spec.BuildArgs[name]; ok {
		return true
	}
	return false
}
