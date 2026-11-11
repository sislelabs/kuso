package projects

import (
	"reflect"
	"slices"
	"sort"
	"strings"

	"kuso/server/internal/kube"
)

// Execution-bearing service fields (SEC-1). Each one picks code that runs
// with the service's env (its secret, addon conns, shared secrets): the
// run command, the image, the release hook, and the build-time command,
// builder images and build args (NIXPACKS_*_CMD among them) that run with
// the build env. Setting one is therefore the same capability as reading
// secret values, and handlers gate it on the project admin role.
//
// Clearing a field back to its default is not gated: the default runs the
// repo's or image's own code, which the field didn't choose.
const (
	ExecFieldRuntime          = "runtime"
	ExecFieldImage            = "image"
	ExecFieldCommand          = "command"
	ExecFieldRelease          = "release"
	ExecFieldFromService      = "fromService"
	ExecFieldBuildCmd         = "static.buildCmd"
	ExecFieldStaticBuilder    = "static.builderImage"
	ExecFieldStaticRuntime    = "static.runtimeImage"
	ExecFieldBuildpackBuilder = "buildpacks.builderImage"
	ExecFieldBuildpackLife    = "buildpacks.lifecycleImage"
	ExecFieldBuildArgs        = "buildArgs"
)

// ExecPatchFields returns the execution-bearing fields req would actually
// change on cur. Values equal to what's stored (a declarative re-send, a
// masked build arg) don't count, so a kuso.yaml re-apply or a settings
// save that leaves them alone isn't gated.
func ExecPatchFields(cur *kube.KusoServiceSpec, req PatchServiceRequest) []string {
	if cur == nil {
		cur = &kube.KusoServiceSpec{}
	}
	var out []string
	if req.Runtime != nil && normRuntime(*req.Runtime) != normRuntime(cur.Runtime) {
		out = append(out, ExecFieldRuntime)
	}
	if req.Image != nil {
		repo := strings.TrimSpace(req.Image.Repository)
		if repo != "" {
			tag := strings.TrimSpace(req.Image.Tag)
			if tag == "" {
				tag = "latest"
			}
			var curRepo, curTag string
			if cur.Image != nil {
				curRepo, curTag = cur.Image.Repository, cur.Image.Tag
			}
			if repo != curRepo || tag != curTag {
				out = append(out, ExecFieldImage)
			}
		}
	}
	if req.Command != nil && len(*req.Command) > 0 && !slices.Equal(*req.Command, cur.Command) {
		out = append(out, ExecFieldCommand)
	}
	if req.Release != nil && !req.Release.Clear && len(req.Release.Command) > 0 {
		var curCmd []string
		if cur.Release != nil {
			curCmd = cur.Release.Command
		}
		if !slices.Equal(req.Release.Command, curCmd) {
			out = append(out, ExecFieldRelease)
		}
	}
	if req.Static != nil {
		var c kube.KusoStaticSpec
		if cur.Static != nil {
			c = *cur.Static
		}
		out = appendIfSet(out, ExecFieldBuildCmd, req.Static.BuildCmd, c.BuildCmd)
		out = appendIfSet(out, ExecFieldStaticBuilder, req.Static.BuilderImage, c.BuilderImage)
		out = appendIfSet(out, ExecFieldStaticRuntime, req.Static.RuntimeImage, c.RuntimeImage)
	}
	if req.Buildpacks != nil {
		var c kube.KusoBuildpacksSpec
		if cur.Buildpacks != nil {
			c = *cur.Buildpacks
		}
		out = appendIfSet(out, ExecFieldBuildpackBuilder, req.Buildpacks.BuilderImage, c.BuilderImage)
		out = appendIfSet(out, ExecFieldBuildpackLife, req.Buildpacks.LifecycleImage, c.LifecycleImage)
	}
	if req.BuildArgs != nil {
		for k, v := range *req.BuildArgs {
			if v == EnvMaskSentinel {
				continue // resolved to the stored value by unmaskBuildArgs
			}
			if old, ok := cur.BuildArgs[k]; !ok || old != v {
				out = append(out, ExecFieldBuildArgs)
				break
			}
		}
	}
	return out
}

// ExecCreateFields returns the execution-bearing fields a new service
// would be created with. A plain repo-built service returns none.
func ExecCreateFields(req CreateServiceRequest) []string {
	var out []string
	if req.Image != nil && strings.TrimSpace(req.Image.Repository) != "" {
		out = append(out, ExecFieldImage)
	}
	if len(req.Command) > 0 {
		out = append(out, ExecFieldCommand)
	}
	if req.Release != nil && !req.Release.Clear && len(req.Release.Command) > 0 {
		out = append(out, ExecFieldRelease)
	}
	if strings.TrimSpace(req.FromService) != "" {
		out = append(out, ExecFieldFromService)
	}
	if req.Static != nil {
		out = appendIfSet(out, ExecFieldBuildCmd, req.Static.BuildCmd, "")
		out = appendIfSet(out, ExecFieldStaticBuilder, req.Static.BuilderImage, "")
		out = appendIfSet(out, ExecFieldStaticRuntime, req.Static.RuntimeImage, "")
	}
	if req.Buildpacks != nil {
		out = appendIfSet(out, ExecFieldBuildpackBuilder, req.Buildpacks.BuilderImage, "")
		out = appendIfSet(out, ExecFieldBuildpackLife, req.Buildpacks.LifecycleImage, "")
	}
	if len(req.BuildArgs) > 0 {
		out = append(out, ExecFieldBuildArgs)
	}
	var names []string
	for _, e := range req.EnvVars {
		names = append(names, e.Name)
	}
	for _, e := range req.EnvValues {
		names = append(names, e.Name)
	}
	return append(out, execEnvFieldsOf(names)...)
}

func appendIfSet(out []string, field, want, have string) []string {
	if want = strings.TrimSpace(want); want != "" && want != have {
		return append(out, field)
	}
	return out
}

func normRuntime(rt string) string {
	if rt = strings.TrimSpace(rt); rt == "" {
		return "dockerfile"
	}
	return rt
}

// Env var names that load or choose code (SEC-1/SEC-2). A runtime reads
// them before the app's own code runs (NODE_OPTIONS=--import=…,
// JAVA_TOOL_OPTIONS=-XX:OnOutOfMemoryError=…, LD_PRELOAD, BASH_ENV), or
// the build reads them to pick its commands, packages or toolchain
// (NIXPACKS_START_CMD, BP_*, PIP_INDEX_URL, GOFLAGS=-toolexec=…). Setting
// one is the same capability as setting `command`, so it carries the same
// project admin gate. Values already stored keep working; only a write
// that changes one is gated.
//
// This is a denylist and can't be complete: an editor who knows a
// runtime's rarer hooks may still find one. This file is the one list;
// the web UI and CLI relay the server's refusal.
var execEnvNames = map[string]bool{
	// Node and Bun.
	"NODE_OPTIONS": true, "NODE_PATH": true, "NODE_REPL_EXTERNAL_MODULE": true,
	"BUN_OPTIONS": true,
	// JVM.
	"JAVA_TOOL_OPTIONS": true, "JDK_JAVA_OPTIONS": true, "_JAVA_OPTIONS": true,
	"JAVA_OPTS": true, "JVM_OPTS": true, "CATALINA_OPTS": true,
	"MAVEN_OPTS": true, "GRADLE_OPTS": true, "SBT_OPTS": true,
	// Shells.
	"BASH_ENV": true, "ENV": true, "SHELLOPTS": true, "BASHOPTS": true,
	"PS4": true, "PROMPT_COMMAND": true, "ZDOTDIR": true, "PATH": true,
	// Python.
	"PYTHONPATH": true, "PYTHONHOME": true, "PYTHONSTARTUP": true,
	"PYTHONINSPECT": true, "PYTHONWARNINGS": true, "PYTHONBREAKPOINT": true,
	"PYTHONUSERBASE": true, "PYTHONPLATLIBDIR": true, "GUNICORN_CMD_ARGS": true,
	"UV_INDEX_URL": true, "UV_EXTRA_INDEX_URL": true, "UV_INDEX": true,
	"UV_DEFAULT_INDEX": true, "UV_FIND_LINKS": true,
	// Perl, Ruby, PHP.
	"PERL5OPT": true, "PERL5LIB": true, "PERLLIB": true, "PERL5DB": true,
	"RUBYOPT": true, "RUBYLIB": true,
	"PHPRC": true, "PHP_INI_SCAN_DIR": true,
	// .NET.
	"DOTNET_STARTUP_HOOKS": true, "DOTNET_ADDITIONAL_DEPS": true,
	// Toolchains the build runs.
	"CC": true, "CXX": true, "CPP": true,
	"GOFLAGS": true, "GOPROXY": true, "GOSUMDB": true, "GONOSUMDB": true,
	"GONOSUMCHECK": true, "GOINSECURE": true, "GOPRIVATE": true, "GOTOOLCHAIN": true,
	"RUSTC": true, "RUSTC_WRAPPER": true, "RUSTFLAGS": true,
	"GIT_SSH": true, "GIT_SSH_COMMAND": true, "GIT_ASKPASS": true, "GIT_EXEC_PATH": true,
	"GIT_EXTERNAL_DIFF": true, "GIT_PROXY_COMMAND": true, "GIT_TEMPLATE_DIR": true,
}

// execEnvPrefixes are matched as name prefixes.
var execEnvPrefixes = []string{
	"LD_",                         // dynamic loader: LD_PRELOAD, LD_AUDIT, LD_LIBRARY_PATH
	"NPM_CONFIG_",                 // npm/pnpm config: script-shell, node-options, registry
	"YARN_",                       // yarn config: yarn-path, plugins, registry
	"NIXPACKS_",                   // nixpacks start/build/install commands and packages
	"BP_", "BPE_", "BPL_", "CNB_", // buildpacks build and launch config
	"PIP_",             // pip index and options
	"CORECLR_", "COR_", // .NET profilers
	"CARGO_",      // cargo wrappers and linkers
	"GIT_CONFIG_", // git config injection (core.sshCommand, …)
}

// IsExecEnvName reports whether an env var named name loads or chooses
// code. Matching ignores case: some tools read lowercase forms too
// (npm_config_*), and a false positive only costs an admin approval.
func IsExecEnvName(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	if execEnvNames[n] {
		return true
	}
	for _, p := range execEnvPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// ExecEnvNameList returns the exact names (sorted) and prefixes that
// IsExecEnvName matches, so clients can warn before the server refuses.
func ExecEnvNameList() (names, prefixes []string) {
	for n := range execEnvNames {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, slices.Clone(execEnvPrefixes)
}

// ExecEnvField is the exec-field label for an env var name.
func ExecEnvField(name string) string { return "env." + name }

// ExecEnvNameFields returns the sorted, de-duplicated exec-field labels
// for the execution-bearing names in names, without comparing values.
// Blind writes (a managed or shared Secret key) use it directly: the
// caller can't see the stored value, and the write replaces it.
func ExecEnvNameFields(names ...string) []string { return execEnvFieldsOf(names) }

func execEnvFieldsOf(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if IsExecEnvName(n) && !seen[n] {
			seen[n] = true
			out = append(out, ExecEnvField(n))
		}
	}
	sort.Strings(out)
	return out
}

// ExecEnvSetFields returns the exec-field labels for the
// execution-bearing vars in sets whose value or ref differs from the
// service's stored spec.envVars entry. Re-sending a value already on the
// CR isn't a change. A value held in the managed Secret compares as
// absent, so writing it again is gated; non-admins can't read it anyway.
func ExecEnvSetFields(cur []kube.KusoEnvVar, sets []EnvVar) []string {
	stored := make(map[string]kube.KusoEnvVar, len(cur))
	for _, e := range cur {
		stored[e.Name] = e
	}
	var changed []string
	for _, e := range sets {
		name := strings.TrimSpace(e.Name)
		if !IsExecEnvName(name) {
			continue
		}
		if old, ok := stored[name]; ok && old.Value == e.Value && envValueFromEqual(old.ValueFrom, e.ValueFrom) {
			continue
		}
		changed = append(changed, name)
	}
	return execEnvFieldsOf(changed)
}

func envValueFromEqual(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
