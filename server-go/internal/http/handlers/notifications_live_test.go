package handlers

import (
	"testing"
	"time"

	"kuso/server/internal/db"
)

func TestDropGhostEvents(t *testing.T) {
	t.Parallel()
	long := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	live := LiveScopes{"demo": {Born: long, Services: map[string]time.Time{"api": long}}, "shop": {Born: long, Services: map[string]time.Time{}}}
	at := long.Add(time.Hour)
	in := []db.NotificationEvent{
		{ID: 1, Project: "demo", Service: "api", CreatedAt: at},
		{ID: 2, Project: "demo", Service: "stucktest", CreatedAt: at},   // deleted service
		{ID: 3, Project: "mkt-hedgedoc", Service: "web", CreatedAt: at}, // deleted project
		{ID: 4, Project: "shop", CreatedAt: at},                         // project-level (addon) event
		{ID: 5, CreatedAt: at},                                          // platform event
	}
	got := dropGhostEvents(in, live)
	var ids []int64
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if len(ids) != 3 || ids[0] != 1 || ids[1] != 4 || ids[2] != 5 {
		t.Errorf("kept %v, want [1 4 5]", ids)
	}
	if n := len(dropGhostEvents([]db.NotificationEvent{{Project: "gone"}}, nil)); n != 1 {
		t.Errorf("nil live set must keep everything, kept %d", n)
	}
}

// A service (or project) re-created under a deleted one's name must not
// inherit its feed: rows are keyed by name, so they clamp to the live
// CR's creation time.
func TestDropGhostEvents_RecreatedNamesake(t *testing.T) {
	t.Parallel()
	projectBorn := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	apiBorn := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	live := LiveScopes{"demo": {Born: projectBorn, Services: map[string]time.Time{"api": apiBorn}}}
	in := []db.NotificationEvent{
		{ID: 1, Project: "demo", Service: "api", CreatedAt: apiBorn.Add(-time.Minute)},           // old api's crash
		{ID: 2, Project: "demo", Service: "api", CreatedAt: apiBorn.Add(time.Second)},            // new api
		{ID: 3, Project: "demo", CreatedAt: projectBorn.Add(-time.Hour)},                         // old project's addon
		{ID: 4, Project: "demo", Service: "api", CreatedAt: apiBorn.Add(400 * time.Millisecond)}, // same second as the (truncated) CR stamp
	}
	got := dropGhostEvents(in, live)
	var ids []int64
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 4 {
		t.Errorf("kept %v, want [2 4]", ids)
	}
}
