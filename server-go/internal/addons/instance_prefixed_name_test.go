package addons

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// DATA-02: an instance addon added as "<project>-db" has its database
// derived from the full name. Resync (the recovery for a lost conn Secret)
// and purge must resolve that same database, not "<project>_db".
func TestInstanceResync_PrefixedNameKeepsOriginalDatabase(t *testing.T) {
	p := uniqueProject(t)
	s, adminDSN := instancePGService(t, p)
	realIdent := pgIdentifier(p, p+"-db")
	dropTestIdent(t, adminDSN, realIdent)
	dropTestIdent(t, adminDSN, pgIdentifier(p, "db"))
	ctx := context.Background()

	if err := instanceAdd(s, p, p+"-db"); err != nil {
		t.Fatalf("add: %v", err)
	}
	execAs(t, connDSN(t, s, p, "db"), `CREATE TABLE orders (id int); INSERT INTO orders VALUES (1)`)

	// Lost conn Secret: resync must rebuild it against the same database.
	if err := s.Kube.Clientset.CoreV1().Secrets("kuso-"+p).Delete(ctx, connSecretName(p+"-db"), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResyncInstanceAddon(ctx, p, "db"); err != nil {
		t.Fatalf("resync: %v", err)
	}
	if dsn := connDSN(t, s, p, "db"); !strings.HasSuffix(strings.SplitN(dsn, "?", 2)[0], "/"+realIdent) {
		t.Fatalf("resync repointed the app at another database: %s (want %s)", dsn, realIdent)
	}
	if dbExists(t, adminDSN, pgIdentifier(p, "db")) {
		t.Fatalf("resync created an empty %s next to the real database", pgIdentifier(p, "db"))
	}
	execAs(t, connDSN(t, s, p, "db"), `SELECT id FROM orders`)

	if err := s.DeleteWith(ctx, p, "db", DeleteOptions{PurgeData: true}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if dbExists(t, adminDSN, realIdent) {
		t.Fatalf("purge left the real database %s behind", realIdent)
	}
}

// DATA-14: an Add that loses the create race to an identical concurrent Add
// must not rotate the winner's role password.
func TestInstanceAdd_LostRaceKeepsWinnersPassword(t *testing.T) {
	p := uniqueProject(t)
	s, adminDSN := instancePGService(t, p)
	dropTestIdent(t, adminDSN, pgIdentifier(p, "db"))
	if err := instanceAdd(s, p, "db"); err != nil {
		t.Fatalf("winner add: %v", err)
	}
	winnerDSN := connDSN(t, s, p, "db")

	// The loser's existence preflight ran before the winner's CR landed.
	dyn := s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient)
	dyn.PrependReactor("get", "kusoaddons", func(a ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, notFound(a.(ktesting.GetAction).GetName())
	})
	if err := instanceAdd(s, p, "db"); !errors.Is(err, ErrConflict) {
		t.Fatalf("loser add: got %v, want ErrConflict", err)
	}
	if err := pingDSN(winnerDSN); err != nil {
		t.Fatalf("winner's credentials stopped working after the losing add: %v", err)
	}
}

func notFound(name string) error {
	return apierrors.NewNotFound(kube.GVRAddons.GroupResource(), name)
}
