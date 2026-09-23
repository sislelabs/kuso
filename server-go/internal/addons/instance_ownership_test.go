package addons

// Postgres-backed tests for instance-shared addon DB ownership. They run
// against a real server (KUSO_TEST_PG_DSN, a superuser DSN) because the
// bugs live in how the provisioner treats databases and roles that
// already exist — a fake can't reproduce that.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/lib/pq"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// instancePGService wires a fake-kube Service to a real admin DSN. Each
// project gets its own execution namespace (kuso-<project>), like a real
// install, so same-named addon CRs in different projects don't collide on
// the kube side and the only shared thing is the Postgres identifier.
func instancePGService(t *testing.T, projects ...string) (*Service, string) {
	t.Helper()
	adminDSN := os.Getenv("KUSO_TEST_PG_DSN")
	if adminDSN == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed test")
	}
	var seeds []seed
	for _, p := range projects {
		seeds = append(seeds, typedSeed(kube.GVRProjects, "KusoProject", &kube.KusoProject{
			ObjectMeta: metav1.ObjectMeta{Name: p, Namespace: "kuso"},
			Spec:       kube.KusoProjectSpec{Namespace: "kuso-" + p, DefaultRepo: &kube.KusoRepoRef{URL: "x"}},
		}))
	}
	s := fakeService(t, seeds...)
	s.Kube.Clientset = kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kuso-instance-shared", Namespace: "kuso"},
		Data:       map[string][]byte{"INSTANCE_ADDON_PG_DSN_ADMIN": []byte(adminDSN)},
	})
	s.NSResolver = kube.NewProjectNamespaceResolver(s.Kube, "kuso")
	return s, adminDSN
}

// uniqueProject returns a fresh project-name prefix so reruns against the
// same server never see a previous run's databases.
func uniqueProject(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "t" + hex.EncodeToString(b)
}

func dropTestIdent(t *testing.T, adminDSN, ident string) {
	t.Helper()
	t.Cleanup(func() {
		db, err := sql.Open("postgres", adminDSN)
		if err != nil {
			return
		}
		defer db.Close()
		_, _ = db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, ident)
		_, _ = db.Exec(`DROP DATABASE IF EXISTS ` + pq.QuoteIdentifier(ident))
		_, _ = db.Exec(`DROP ROLE IF EXISTS ` + pq.QuoteIdentifier(ident))
	})
}

func connDSN(t *testing.T, s *Service, project, addon string) string {
	t.Helper()
	sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso-"+project).Get(context.Background(), connSecretName(addonCRName(project, addon)), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("conn secret for %s/%s: %v", project, addon, err)
	}
	return string(sec.Data["DIRECT_URL"])
}

func pingDSN(dsn string) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Ping()
}

func execAs(t *testing.T, dsn, stmt string) {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}

func adminExec(t *testing.T, adminDSN string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("postgres", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, st := range stmts {
		if _, err := db.Exec(st); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
}

func dbExists(t *testing.T, adminDSN, name string) bool {
	t.Helper()
	db, err := sql.Open("postgres", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pg_database WHERE datname = $1`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func instanceAdd(s *Service, project, addon string) error {
	_, err := s.Add(context.Background(), project, CreateAddonRequest{Name: addon, Kind: "postgres", UseInstanceAddon: "pg"})
	return err
}

// ("p", "api-db") and ("p-api", "db") both map to the Postgres identifier
// p_api_db. The second project must be refused instead of adopting the
// first project's database and rotating its role password — even after
// the first project deleted its addon (the DB is kept for reattach, so no
// CR is left to point at it; only the stamp on the DB knows the owner).
func TestInstanceAdd_RefusesDBOwnedByAnotherProject(t *testing.T) {
	p1 := uniqueProject(t)
	p2 := p1 + "-api"
	s, adminDSN := instancePGService(t, p1, p2)
	ident := pgIdentifier(p1, "api-db")
	if ident != pgIdentifier(p2, "db") {
		t.Fatalf("fixture no longer collides: %q vs %q", ident, pgIdentifier(p2, "db"))
	}
	dropTestIdent(t, adminDSN, ident)

	if err := instanceAdd(s, p1, "api-db"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	firstDSN := connDSN(t, s, p1, "api-db")
	if err := s.Delete(context.Background(), p1, "api-db"); err != nil {
		t.Fatalf("delete first addon: %v", err)
	}

	err := instanceAdd(s, p2, "db")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second project's add: got %v, want ErrConflict", err)
	}
	if perr := pingDSN(firstDSN); perr != nil {
		t.Errorf("first project's stored credentials stopped working after the refused add: %v", perr)
	}
}

// A legacy database (provisioned before ownership markers existed) with
// another project's live addon CR mapping to it must also be refused.
func TestInstanceAdd_RefusesUnmarkedDBClaimedByAnotherLiveAddon(t *testing.T) {
	p1 := uniqueProject(t)
	p2 := p1 + "-api"
	s, adminDSN := instancePGService(t, p1, p2)
	ident := pgIdentifier(p1, "api-db")
	dropTestIdent(t, adminDSN, ident)
	adminExec(t, adminDSN,
		`CREATE DATABASE `+pq.QuoteIdentifier(ident),
		`CREATE ROLE `+pq.QuoteIdentifier(ident)+` WITH LOGIN PASSWORD 'legacy'`)
	dyn := s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient)
	legacy := typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: p1 + "-api-db", Namespace: "kuso-" + p1,
			Labels: map[string]string{kube.LabelProject: p1, "kuso.sislelabs.com/addon": "api-db"}},
		Spec: kube.KusoAddonSpec{Project: p1, Kind: "postgres", UseInstanceAddon: "pg"},
	})
	if err := dyn.Tracker().Create(kube.GVRAddons, legacy.obj, "kuso-"+p1); err != nil {
		t.Fatal(err)
	}

	if err := instanceAdd(s, p2, "db"); !errors.Is(err, ErrConflict) {
		t.Fatalf("add over a legacy DB another live addon owns: got %v, want ErrConflict", err)
	}
	u, _ := url.Parse(adminDSN)
	u.User = url.UserPassword(ident, "legacy")
	u.Path = "/" + ident
	if err := pingDSN(u.String()); err != nil {
		t.Errorf("legacy owner's password was rotated by the refused add: %v", err)
	}
}

// The intended "delete keeps the DB, re-add reattaches" flow, for both a
// DB this code provisioned and a legacy unmarked one nobody else claims.
func TestInstanceAdd_ReattachesOwnKeptDB(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			p := uniqueProject(t)
			s, adminDSN := instancePGService(t, p)
			ident := pgIdentifier(p, "db")
			dropTestIdent(t, adminDSN, ident)
			if legacy {
				adminExec(t, adminDSN,
					`CREATE DATABASE `+pq.QuoteIdentifier(ident),
					`CREATE ROLE `+pq.QuoteIdentifier(ident)+` WITH LOGIN PASSWORD 'legacy'`)
			} else if err := instanceAdd(s, p, "db"); err != nil {
				t.Fatalf("first add: %v", err)
			} else {
				execAs(t, connDSN(t, s, p, "db"), `CREATE TABLE keep (v text); INSERT INTO keep VALUES ('data')`)
				if err := s.Delete(context.Background(), p, "db"); err != nil {
					t.Fatalf("delete: %v", err)
				}
			}
			if err := instanceAdd(s, p, "db"); err != nil {
				t.Fatalf("re-add of own kept DB: %v", err)
			}
			if err := s.ResyncInstanceAddon(context.Background(), p, "db"); err != nil {
				t.Fatalf("resync: %v", err)
			}
			if !legacy {
				execAs(t, connDSN(t, s, p, "db"), `SELECT v FROM keep`)
			}
		})
	}
}

// Every instance DB on a pre-upgrade cluster is unmarked and has its own
// live CR. Resync (and the first stamp) must adopt it, not see its own CR
// as a competing owner.
func TestInstanceResync_AdoptsOwnUnmarkedLiveDB(t *testing.T) {
	p := uniqueProject(t)
	s, adminDSN := instancePGService(t, p)
	ident := pgIdentifier(p, "db")
	dropTestIdent(t, adminDSN, ident)
	adminExec(t, adminDSN,
		`CREATE DATABASE `+pq.QuoteIdentifier(ident),
		`CREATE ROLE `+pq.QuoteIdentifier(ident)+` WITH LOGIN PASSWORD 'legacy'`)
	own := typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: p + "-db", Namespace: "kuso-" + p,
			Labels: map[string]string{kube.LabelProject: p, "kuso.sislelabs.com/addon": "db"}},
		Spec: kube.KusoAddonSpec{Project: p, Kind: "postgres", UseInstanceAddon: "pg"},
	})
	if err := s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).Tracker().Create(kube.GVRAddons, own.obj, "kuso-"+p); err != nil {
		t.Fatal(err)
	}
	if err := s.ResyncInstanceAddon(context.Background(), p, "db"); err != nil {
		t.Fatalf("resync of own unmarked DB: %v", err)
	}
	execAs(t, connDSN(t, s, p, "db"), `SELECT 1`)
}
