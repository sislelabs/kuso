package handlers

import (
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
