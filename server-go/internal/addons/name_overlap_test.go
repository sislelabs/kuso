package addons

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// SEC-3: "acme" adding "prod-db" would create acme-prod-db-conn, the
// Secret a pending ${{ db.X }} ref in project "acme-prod" names. The
// create is refused while acme-prod exists; unambiguous names still add.
func TestAdd_RefusesNameAnotherProjectOwns(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("acme"), seedProj("acme-prod"))
	ctx := context.Background()
	if _, err := s.Add(ctx, "acme", CreateAddonRequest{Name: "prod-db", Kind: "postgres"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("acme adds prod-db: want ErrConflict, got %v", err)
	}
	if _, err := s.Add(ctx, "acme", CreateAddonRequest{Name: "main-db", Kind: "postgres"}); err != nil {
		t.Fatalf("acme adds main-db: %v", err)
	}
	if _, err := s.Add(ctx, "acme-prod", CreateAddonRequest{Name: "db", Kind: "postgres"}); err != nil {
		t.Fatalf("acme-prod adds db: %v", err)
	}
}

// Projects in their own namespaces can't bind each other's Secrets, so
// the overlap doesn't block there.
func TestAdd_NameOverlapIgnoresOtherNamespaces(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("acme"), typedSeed(kube.GVRProjects, "KusoProject", &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-prod", Namespace: "kuso"},
		Spec:       kube.KusoProjectSpec{Namespace: "kuso-acme-prod", DefaultRepo: &kube.KusoRepoRef{URL: "x"}},
	}))
	s.NSResolver = kube.NewProjectNamespaceResolver(s.Kube, "kuso")
	if _, err := s.Add(context.Background(), "acme", CreateAddonRequest{Name: "prod-db", Kind: "postgres"}); err != nil {
		t.Fatalf("acme adds prod-db with acme-prod in its own namespace: %v", err)
	}
}
