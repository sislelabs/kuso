package handlers

import (
	"context"
	"testing"

	"kuso/server/internal/db"
)

// Rows written before actors were recorded by name carry the raw user id
// (live: ACTOR 3cb3589ec763fa2cfff589acb1616fb3). The list/get endpoints
// map those back to a username; unknown actors pass through untouched.
func TestResolveRevisionActors(t *testing.T) {
	t.Parallel()
	users := map[string]string{"3cb3589ec763fa2cfff589acb1616fb3": "ivo"}
	calls := 0
	lookup := func(_ context.Context, id string) (string, bool) {
		calls++
		u, ok := users[id]
		return u, ok
	}
	revs := []db.Revision{
		{Actor: "3cb3589ec763fa2cfff589acb1616fb3"},
		{Actor: "3cb3589ec763fa2cfff589acb1616fb3"},
		{Actor: "alice"},
		{Actor: ""},
	}
	resolveRevisionActors(context.Background(), revs, lookup)
	want := []string{"ivo", "ivo", "alice", ""}
	for i, w := range want {
		if revs[i].Actor != w {
			t.Errorf("revs[%d].Actor = %q, want %q", i, revs[i].Actor, w)
		}
	}
	if calls != 2 {
		t.Errorf("lookup called %d times, want 2 (once per distinct non-empty actor)", calls)
	}
}
