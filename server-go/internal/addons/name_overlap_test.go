package addons

import (
	"context"
	"errors"
	"testing"
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
