package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

const credURL = "https://kuso-deploy:gldt-secret@gitlab.com/org/app.git"
const strippedURL = "https://gitlab.com/org/app.git"

// TestUpdate_RedactedRepoURLPreservesCredentials: API reads redact the
// deploy-token userinfo, so a settings save that echoes the redacted
// URL back must keep the stored credentials — otherwise every project
// edit by a non-admin silently breaks cloning.
func TestUpdate_RedactedRepoURLPreservesCredentials(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: credURL, DefaultBranch: "main"},
	}))

	out, err := s.Update(context.Background(), "alpha", UpdateProjectRequest{
		DefaultRepo: &CreateProjectRepoSpec{URL: strippedURL, DefaultBranch: "develop"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.Spec.DefaultRepo.URL != credURL {
		t.Errorf("credentials clobbered: url = %q, want stored %q", out.Spec.DefaultRepo.URL, credURL)
	}
	if out.Spec.DefaultRepo.DefaultBranch != "develop" {
		t.Errorf("branch not updated alongside preserved URL: %q", out.Spec.DefaultRepo.DefaultBranch)
	}
}

// TestUpdate_NewRepoURLReplaces: a genuinely different URL (even
// credential-less) must replace the stored one — preservation only
// kicks in when the incoming URL is the redacted form of the stored.
func TestUpdate_NewRepoURLReplaces(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: credURL},
	}))

	const other = "https://gitlab.com/org/other.git"
	out, err := s.Update(context.Background(), "alpha", UpdateProjectRequest{
		DefaultRepo: &CreateProjectRepoSpec{URL: other},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.Spec.DefaultRepo.URL != other {
		t.Errorf("url = %q, want replacement %q", out.Spec.DefaultRepo.URL, other)
	}
}

// TestUpdate_NewCredentialsReplace: supplying a URL WITH fresh
// credentials always wins (token rotation path).
func TestUpdate_NewCredentialsReplace(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: credURL},
	}))

	const rotated = "https://kuso-deploy:gldt-rotated@gitlab.com/org/app.git"
	out, err := s.Update(context.Background(), "alpha", UpdateProjectRequest{
		DefaultRepo: &CreateProjectRepoSpec{URL: rotated},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.Spec.DefaultRepo.URL != rotated {
		t.Errorf("url = %q, want rotated %q", out.Spec.DefaultRepo.URL, rotated)
	}
}
