package projects

import (
	"context"
	"slices"
	"testing"

	"kuso/server/internal/kube"
)

// A settings save or kuso.yml re-apply re-sends unchanged values (and
// masked build args); only a real change to an execution-bearing field
// may count, or every editor save of an unrelated field would 403.
func TestExecPatchFields_OnlyRealChanges(t *testing.T) {
	cur := &kube.KusoServiceSpec{
		Image:     &kube.KusoImage{Repository: "ghcr.io/acme/web", Tag: "latest"},
		BuildArgs: map[string]string{"TOKEN": "abc"},
	}
	str := func(s string) *string { return &s }
	same := PatchServiceRequest{
		Runtime:   str("dockerfile"),
		Image:     &ServiceImageSpec{Repository: "ghcr.io/acme/web"},
		BuildArgs: &map[string]string{"TOKEN": EnvMaskSentinel},
	}
	if got := ExecPatchFields(cur, same); len(got) != 0 {
		t.Errorf("unchanged values flagged: %v", got)
	}
	changed := PatchServiceRequest{
		Image:     &ServiceImageSpec{Repository: "ghcr.io/acme/web", Tag: "2"},
		BuildArgs: &map[string]string{"NIXPACKS_BUILD_CMD": "printenv"},
	}
	got := ExecPatchFields(cur, changed)
	if !slices.Contains(got, ExecFieldImage) || !slices.Contains(got, ExecFieldBuildArgs) {
		t.Errorf("ExecPatchFields = %v, want image and buildArgs", got)
	}
}

// SEC-1 (2026-11-12): the repo, branch, path and Dockerfile pick the code
// a service's secrets run with. A redacted or reformatted echo of the
// stored repo isn't a change; clearing back to the default isn't gated.
func TestExecPatchFields_Repo(t *testing.T) {
	cur := &kube.KusoServiceSpec{
		Repo:       &kube.KusoRepoRef{URL: "https://bot:tok@github.com/acme/web.git", DefaultBranch: "main", Path: "apps/web"},
		Dockerfile: "Dockerfile",
	}
	str := func(s string) *string { return &s }
	for _, req := range []PatchServiceRequest{
		{Repo: &PatchRepoRequest{URL: "https://github.com/acme/web", Branch: "main", Path: "./apps/web/"}},
		{Repo: &PatchRepoRequest{URL: "git@github.com:ACME/web.git", Branch: "main", Path: "apps/web"}},
		{Repo: &PatchRepoRequest{URL: "https://github.com/acme/web", Path: "apps/web"}},
		{Repo: &PatchRepoRequest{}},
		{Dockerfile: str("")},
		{Dockerfile: str("Dockerfile")},
	} {
		if got := ExecPatchFields(cur, req); len(got) != 0 {
			t.Errorf("%+v flagged: %v", req.Repo, got)
		}
	}
	for _, c := range []struct {
		req  PatchServiceRequest
		want string
	}{
		{PatchServiceRequest{Repo: &PatchRepoRequest{URL: "https://gitlab.com/attacker/x", Branch: "main", Path: "apps/web"}}, ExecFieldRepo},
		{PatchServiceRequest{Repo: &PatchRepoRequest{URL: "https://attacker.example/acme/web", Branch: "main", Path: "apps/web"}}, ExecFieldRepo},
		{PatchServiceRequest{Repo: &PatchRepoRequest{URL: "https://github.com/acme/web", Branch: "evil", Path: "apps/web"}}, ExecFieldRepoBranch},
		{PatchServiceRequest{Repo: &PatchRepoRequest{URL: "https://github.com/acme/web", Branch: "main", Path: "tools/x"}}, ExecFieldRepoPath},
		{PatchServiceRequest{Dockerfile: str("Dockerfile.evil")}, ExecFieldDockerfile},
	} {
		if got := ExecPatchFields(cur, c.req); !slices.Contains(got, c.want) {
			t.Errorf("ExecPatchFields(%+v) = %v, want %s", c.req.Repo, got, c.want)
		}
	}
}

// Env names that load or choose code are exec fields; ordinary app
// config must stay editor-writable.
func TestIsExecEnvName(t *testing.T) {
	for name, want := range map[string]bool{
		"NODE_OPTIONS": true, "node_options": true, "JAVA_TOOL_OPTIONS": true,
		"LD_PRELOAD": true, "BASH_ENV": true, "PYTHONSTARTUP": true,
		"PYTHONWARNINGS": true, "PERL5OPT": true, "RUBYOPT": true,
		"NIXPACKS_START_CMD": true, "NIXPACKS_BUILD_CMD": true, "BP_NODE_RUN_SCRIPTS": true,
		"BPE_DEFAULT_X": true, "npm_config_script_shell": true, "GOFLAGS": true,
		"GIT_CONFIG_COUNT": true, "DOTNET_STARTUP_HOOKS": true,
		"DATABASE_URL": false, "NODE_ENV": false, "PYTHONUNBUFFERED": false,
		"UV_THREADPOOL_SIZE": false, "GIT_SHA": false, "API_KEY": false, "ENVIRONMENT": false,
	} {
		if got := IsExecEnvName(name); got != want {
			t.Errorf("IsExecEnvName(%q) = %v, want %v", name, got, want)
		}
	}
}

// SEC-4: replaying a revision is a write; it reports the exec fields the
// replay would change against the live object, not the stored ones.
func TestRevisionExecFields(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Port: 8080, Command: []string{"./run"},
			EnvVars: []kube.KusoEnvVar{{Name: "NODE_OPTIONS", Value: "--max-old-space-size=512"}}}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	cases := []struct {
		name, kind, env, snap string
		want                  []string
	}{
		{"old command", "service", "", `{"patch":{"command":["printenv"]}}`, []string{ExecFieldCommand}},
		{"same command", "service", "", `{"patch":{"command":["./run"],"port":1}}`, nil},
		{"old NODE_OPTIONS", "service", "", `{"op":"service.envVars","envVars":[{"name":"NODE_OPTIONS","value":"--import=x"}]}`, []string{"env.NODE_OPTIONS"}},
		{"masked NODE_OPTIONS", "service", "", `{"op":"service.envVars","envVars":[{"name":"NODE_OPTIONS","value":"` + EnvMaskSentinel + `"}]}`, nil},
		{"foreign image onto env", "service", "production", `{"patch":{"image":{"repository":"evil/img","tag":"1"}}}`, []string{ExecFieldImage}},
		{"informational", "service", "", `{"op":"x","informational":true,"patch":{"command":["printenv"]}}`, nil},
	}
	for _, c := range cases {
		got, err := s.RevisionExecFields(context.Background(), "alpha", c.kind, "web", c.env, []byte(c.snap))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: RevisionExecFields = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExecEnvSetFields(t *testing.T) {
	ref := map[string]any{"secretKeyRef": map[string]any{"name": "a-conn", "key": "K"}}
	cur := []kube.KusoEnvVar{
		{Name: "NODE_OPTIONS", Value: "--max-old-space-size=512"},
		{Name: "RUBYOPT", ValueFrom: ref},
	}
	cases := []struct {
		name string
		sets []EnvVar
		want []string
	}{
		{"unchanged literal", []EnvVar{{Name: "NODE_OPTIONS", Value: "--max-old-space-size=512"}}, nil},
		{"unchanged ref", []EnvVar{{Name: "RUBYOPT", ValueFrom: ref}}, nil},
		{"changed literal", []EnvVar{{Name: "NODE_OPTIONS", Value: "--import=x"}}, []string{"env.NODE_OPTIONS"}},
		{"new name", []EnvVar{{Name: "LD_PRELOAD", Value: "/x.so"}, {Name: "DATABASE_URL", Value: "x"}}, []string{"env.LD_PRELOAD"}},
		{"ref to literal", []EnvVar{{Name: "RUBYOPT", Value: "-rx"}}, []string{"env.RUBYOPT"}},
	}
	for _, c := range cases {
		if got := ExecEnvSetFields(cur, c.sets); !slices.Equal(got, c.want) {
			t.Errorf("%s: ExecEnvSetFields = %v, want %v", c.name, got, c.want)
		}
	}
	got := ExecCreateFields(CreateServiceRequest{EnvValues: []EnvValue{{Name: "NIXPACKS_START_CMD", Value: "env"}}})
	if !slices.Equal(got, []string{"env.NIXPACKS_START_CMD"}) {
		t.Errorf("ExecCreateFields(env) = %v", got)
	}
}
