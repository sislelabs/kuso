package handlers

import (
	"encoding/json"
	"reflect"
	"testing"

	apiv1 "github.com/sislelabs/kuso/api/apiv1"
)

// TestApiv1CreateServiceToDomain_MapsExtendedFields guards finding 29:
// release hooks, build args, public env, and security context existed on
// the internal create request but were silently dropped by the shared
// apiv1 DTO — callers got 201 with the config missing. Every field the
// internal create path supports must survive the conversion.
func TestApiv1CreateServiceToDomain_MapsExtendedFields(t *testing.T) {
	t.Parallel()
	esc := true
	in := apiv1.CreateServiceRequest{
		Name:    "web",
		Runtime: "dockerfile",
		Repo:    &apiv1.ServiceRepoSpec{URL: "https://github.com/x/y", Path: "apps/web"},
		Release: &apiv1.ServiceRelease{
			Command:        []string{"bin/rails", "db:migrate"},
			TimeoutSeconds: 300,
		},
		BuildArgs: map[string]string{"NODE_ENV": "production"},
		PublicEnv: []string{"NEXT_PUBLIC_API_URL"},
		SecurityContext: &apiv1.ServiceSecurityContext{
			Capabilities:             &apiv1.ServiceCapabilities{Add: []string{"NET_BIND_SERVICE"}},
			AllowPrivilegeEscalation: &esc,
		},
	}

	out := apiv1CreateServiceToDomain(in)

	if out.Release == nil || !reflect.DeepEqual(out.Release.Command, in.Release.Command) || out.Release.TimeoutSeconds != 300 {
		t.Errorf("release dropped/mangled: %+v", out.Release)
	}
	if !reflect.DeepEqual(out.BuildArgs, in.BuildArgs) {
		t.Errorf("buildArgs dropped: %+v", out.BuildArgs)
	}
	if !reflect.DeepEqual(out.PublicEnv, in.PublicEnv) {
		t.Errorf("publicEnv dropped: %+v", out.PublicEnv)
	}
	if out.SecurityContext == nil ||
		out.SecurityContext.Capabilities == nil ||
		!reflect.DeepEqual(out.SecurityContext.Capabilities.Add, []string{"NET_BIND_SERVICE"}) ||
		out.SecurityContext.AllowPrivilegeEscalation == nil ||
		!*out.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("securityContext dropped/mangled: %+v", out.SecurityContext)
	}
	if out.Repo == nil || out.Repo.Path != "apps/web" {
		t.Errorf("repo.path dropped: %+v", out.Repo)
	}
}

// TestApiv1ProjectConversions_MapRepoPath guards finding 37: the public
// DTO exposes defaultRepo.path but the create/update conversions copied
// only URL + default branch.
func TestApiv1ProjectConversions_MapRepoPath(t *testing.T) {
	t.Parallel()
	repo := &apiv1.RepoRef{URL: "https://github.com/x/mono", DefaultBranch: "main", Path: "services/api"}

	created := apiv1CreateToDomain(apiv1.CreateProjectRequest{Name: "p", DefaultRepo: repo})
	if created.DefaultRepo == nil || created.DefaultRepo.Path != "services/api" {
		t.Errorf("create: defaultRepo.path dropped: %+v", created.DefaultRepo)
	}

	updated := apiv1UpdateToDomain(apiv1.UpdateProjectRequest{DefaultRepo: repo})
	if updated.DefaultRepo == nil || updated.DefaultRepo.Path != "services/api" {
		t.Errorf("update: defaultRepo.path dropped: %+v", updated.DefaultRepo)
	}
}

// The web's new-service form sends the picked repo's default branch; the
// wire type had no field for it, so decoding silently dropped it.
func TestApiv1CreateServiceCarriesRepoDefaultBranch(t *testing.T) {
	var req apiv1.CreateServiceRequest
	body := `{"name":"web","repo":{"url":"https://github.com/x/legacy","defaultBranch":"master"}}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	out := apiv1CreateServiceToDomain(req)
	if out.Repo == nil || out.Repo.DefaultBranch != "master" {
		t.Errorf("repo = %+v, want defaultBranch master", out.Repo)
	}
}

// resources absent = server default pod size; {} = explicitly none. The
// mapper must keep that distinction instead of collapsing both to nil.
func TestApiv1CreateServiceCarriesResources(t *testing.T) {
	var absent, empty, set apiv1.CreateServiceRequest
	for body, dst := range map[string]*apiv1.CreateServiceRequest{
		`{"name":"web"}`:                                         &absent,
		`{"name":"web","resources":{}}`:                          &empty,
		`{"name":"web","resources":{"limits":{"memory":"1Gi"}}}`: &set,
	} {
		if err := json.Unmarshal([]byte(body), dst); err != nil {
			t.Fatal(err)
		}
	}
	if out := apiv1CreateServiceToDomain(absent); out.Resources != nil {
		t.Errorf("absent resources mapped to %v, want nil", *out.Resources)
	}
	if out := apiv1CreateServiceToDomain(empty); out.Resources == nil || len(*out.Resources) != 0 {
		t.Errorf("empty resources mapped to %v, want non-nil empty", out.Resources)
	}
	out := apiv1CreateServiceToDomain(set)
	want := map[string]any{"limits": map[string]any{"memory": "1Gi"}}
	if out.Resources == nil || !reflect.DeepEqual(*out.Resources, want) {
		t.Errorf("resources = %v, want %v", out.Resources, want)
	}
}

func TestApiv1CreateServiceCarriesSleepNonProduction(t *testing.T) {
	var in apiv1.CreateServiceRequest
	if err := json.Unmarshal([]byte(`{"name":"web","sleep":{"afterMinutes":10,"nonProduction":"off"}}`), &in); err != nil {
		t.Fatal(err)
	}
	out := apiv1CreateServiceToDomain(in)
	if out.Sleep == nil || out.Sleep.NonProduction != "off" || out.Sleep.AfterMinutes != 10 {
		t.Fatalf("sleep = %+v, want afterMinutes 10 + nonProduction off", out.Sleep)
	}
}

// The web wizard sends image.pullSecret; it was dropped here, so private
// images hit ImagePullBackOff on the first deploy.
func TestApiv1CreateServiceCarriesImagePullSecret(t *testing.T) {
	got := apiv1CreateServiceToDomain(apiv1.CreateServiceRequest{
		Name:  "web",
		Image: &apiv1.ServiceImage{Repository: "ghcr.io/acme/web", Tag: "v1", PullSecret: "ghcr.io"},
	})
	if got.Image == nil || got.Image.PullSecret == nil || *got.Image.PullSecret != "ghcr.io" {
		t.Fatalf("pullSecret not mapped: %+v", got.Image)
	}
}

func TestApiv1CreateServiceCarriesGitHubInstallation(t *testing.T) {
	got := apiv1CreateServiceToDomain(apiv1.CreateServiceRequest{
		Name:   "web",
		GitHub: &apiv1.GitHubInstallationRef{InstallationID: 42},
	})
	if got.GitHub == nil || got.GitHub.InstallationID != 42 {
		t.Fatalf("github.installationId not mapped: %+v", got.GitHub)
	}
}
