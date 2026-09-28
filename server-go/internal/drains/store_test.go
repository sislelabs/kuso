package drains

import (
	"context"
	"errors"
	"os"
	"testing"

	"kuso/server/internal/db"
)

func TestSettingStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed test")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	wipe := func() {
		_, _ = d.ExecContext(ctx, `DELETE FROM "Setting" WHERE key LIKE 'drain.%' OR key = 'drainage'`)
	}
	wipe()
	t.Cleanup(wipe)
	s := &SettingStore{DB: d}

	a := &Drain{Name: "a", Type: TypeLoki, URL: "https://loki.example", Enabled: true, Headers: map[string]string{"X-Scope-OrgID": "t1"}}
	if err := s.Put(ctx, a, "admin"); err != nil {
		t.Fatal(err)
	}
	b := &Drain{Name: "b", Type: TypeHTTP, URL: "https://hook.example", Project: "shop", Secret: "s"}
	if err := s.Put(ctx, b, "admin"); err != nil {
		t.Fatal(err)
	}
	// An unrelated Setting row must not be picked up by the prefix scan.
	if err := d.SetSetting(ctx, "drainage", `{"id":"decoy","enabled":true}`, "admin"); err != nil {
		t.Fatal(err)
	}

	all, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != a.ID || all[1].Secret != "s" || all[0].Headers["X-Scope-OrgID"] != "t1" {
		t.Fatalf("list = %+v", all)
	}
	b.Enabled = true
	if err := s.Put(ctx, b, "admin"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, b.ID)
	if err != nil || !got.Enabled || got.CreatedAt.IsZero() {
		t.Fatalf("get after update = %+v, %v", got, err)
	}
	if err := s.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err=%v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted err=%v", err)
	}
}
