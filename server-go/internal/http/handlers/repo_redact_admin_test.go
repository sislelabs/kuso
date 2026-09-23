package handlers

import (
	"context"
	"strings"
	"testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

// Clone credentials in repo URLs are redacted for every caller, admins
// included: nobody needs the token back (writes preserve it on a redacted
// echo), and `kuso get projects` printed it in the REPO column.
func TestRepoRedaction_AppliesToInstanceAdmins(t *testing.T) {
	t.Parallel()
	const tokenURL = "https://kuso-deploy:gldt-SECRET@gitlab.com/org/app.git"
	admin := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}
	ctx := auth.WithClaimsForTest(context.Background(), admin)

	p := &kube.KusoProject{}
	p.Name = "alpha"
	p.Spec.DefaultRepo = &kube.KusoRepoRef{URL: tokenURL}
	redactProjectRepoIfNeeded(ctx, nil, p)

	svc := &kube.KusoService{}
	svc.Spec.Repo = &kube.KusoRepoRef{URL: tokenURL}
	redactServiceRepoIfNeeded(ctx, nil, "alpha", svc)

	svcs := []kube.KusoService{{Spec: kube.KusoServiceSpec{Repo: &kube.KusoRepoRef{URL: tokenURL}}}}
	redactServicesRepoIfNeeded(ctx, nil, "alpha", svcs)

	for name, got := range map[string]string{
		"project defaultRepo": p.Spec.DefaultRepo.URL,
		"service repo":        svc.Spec.Repo.URL,
		"services list repo":  svcs[0].Spec.Repo.URL,
	} {
		if strings.Contains(got, "gldt-SECRET") {
			t.Errorf("%s still carries the token for an instance admin", name)
		}
		if got != "https://gitlab.com/org/app.git" {
			t.Errorf("%s = %q, want the URL without userinfo", name, got)
		}
	}
}

func TestRepoRedaction_DoesNotMutateStoredRef(t *testing.T) {
	t.Parallel()
	const tokenURL = "https://kuso-deploy:gldt-SECRET@gitlab.com/org/app.git"
	stored := &kube.KusoRepoRef{URL: tokenURL}
	p := &kube.KusoProject{}
	p.Spec.DefaultRepo = stored
	redactProjectRepoIfNeeded(context.Background(), nil, p)
	if stored.URL != tokenURL {
		t.Errorf("redaction rewrote the shared ref in place: %q", stored.URL)
	}
}
