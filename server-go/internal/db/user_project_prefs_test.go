package db

import (
	"context"
	"testing"
)

func TestUserProjectPrefs_StarFolderLifecycle(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	const uid = "user-1"

	// Empty to start.
	got, err := d.ListUserProjectPrefs(ctx, uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 prefs, got %d", len(got))
	}

	// Star a project.
	if err := d.SetUserProjectPref(ctx, uid, "alpha", true, ""); err != nil {
		t.Fatalf("star alpha: %v", err)
	}
	// File another in a folder (unstarred).
	if err := d.SetUserProjectPref(ctx, uid, "beta", false, "Client Work"); err != nil {
		t.Fatalf("file beta: %v", err)
	}
	// Starred AND filed.
	if err := d.SetUserProjectPref(ctx, uid, "gamma", true, "Client Work"); err != nil {
		t.Fatalf("star+file gamma: %v", err)
	}

	got, err = d.ListUserProjectPrefs(ctx, uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byProj := map[string]UserProjectPref{}
	for _, p := range got {
		byProj[p.Project] = p
	}
	if len(byProj) != 3 {
		t.Fatalf("want 3 prefs, got %d (%v)", len(byProj), got)
	}
	if !byProj["alpha"].Starred || byProj["alpha"].Folder != "" {
		t.Errorf("alpha: %+v, want starred + no folder", byProj["alpha"])
	}
	if byProj["beta"].Starred || byProj["beta"].Folder != "Client Work" {
		t.Errorf("beta: %+v, want unstarred + Client Work", byProj["beta"])
	}
	if !byProj["gamma"].Starred || byProj["gamma"].Folder != "Client Work" {
		t.Errorf("gamma: %+v, want starred + Client Work", byProj["gamma"])
	}

	// Upsert: unstar alpha but keep no folder → reverts to default → row deleted.
	if err := d.SetUserProjectPref(ctx, uid, "alpha", false, ""); err != nil {
		t.Fatalf("unstar alpha: %v", err)
	}
	got, _ = d.ListUserProjectPrefs(ctx, uid)
	for _, p := range got {
		if p.Project == "alpha" {
			t.Errorf("alpha should be gone after reverting to default, got %+v", p)
		}
	}

	// Rename folder "Client Work" → "Clients" moves beta + gamma.
	n, err := d.RenameUserFolder(ctx, uid, "Client Work", "Clients")
	if err != nil {
		t.Fatalf("rename folder: %v", err)
	}
	if n != 2 {
		t.Errorf("rename touched %d rows, want 2", n)
	}
	got, _ = d.ListUserProjectPrefs(ctx, uid)
	for _, p := range got {
		if p.Project == "beta" && p.Folder != "Clients" {
			t.Errorf("beta folder = %q, want Clients", p.Folder)
		}
	}

	// Per-user isolation: a different user sees nothing.
	other, err := d.ListUserProjectPrefs(ctx, "user-2")
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("user-2 should have 0 prefs, got %d", len(other))
	}

	// Clear is idempotent.
	if err := d.ClearUserProjectPref(ctx, uid, "beta"); err != nil {
		t.Fatalf("clear beta: %v", err)
	}
	if err := d.ClearUserProjectPref(ctx, uid, "beta"); err != nil {
		t.Fatalf("clear beta again (idempotent): %v", err)
	}
}

// TestRenameUserFolder_UnfileDeletesUnstarredRows locks in the "no row =
// default" invariant for the unfile (to="") path: an unstarred project
// that gets unfiled must have its row DELETED (not left as a NULL-folder
// row), while a starred+filed project keeps its row (loses only the
// folder). Otherwise stale rows resurface as a phantom "Unfiled" section.
func TestRenameUserFolder_UnfileDeletesUnstarredRows(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	const uid = "user-unfile"

	// Two projects in the same folder: one unstarred, one starred.
	if err := d.SetUserProjectPref(ctx, uid, "plain", false, "Work"); err != nil {
		t.Fatalf("file plain: %v", err)
	}
	if err := d.SetUserProjectPref(ctx, uid, "fav", true, "Work"); err != nil {
		t.Fatalf("star+file fav: %v", err)
	}

	// Unfile the whole folder.
	if _, err := d.RenameUserFolder(ctx, uid, "Work", ""); err != nil {
		t.Fatalf("unfile: %v", err)
	}

	got, err := d.ListUserProjectPrefs(ctx, uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byProj := map[string]UserProjectPref{}
	for _, p := range got {
		byProj[p.Project] = p
	}
	// "plain" was unstarred + now unfiled → default → row gone.
	if _, ok := byProj["plain"]; ok {
		t.Errorf("plain should be deleted (unstarred + unfiled), got %+v", byProj["plain"])
	}
	// "fav" was starred → row stays, folder cleared.
	fav, ok := byProj["fav"]
	if !ok {
		t.Fatalf("fav row should survive (still starred)")
	}
	if !fav.Starred || fav.Folder != "" {
		t.Errorf("fav: %+v, want starred + no folder", fav)
	}
}
