package handlers

import (
	"testing"

	"kuso/server/internal/db"
)

func TestDropGhostEvents(t *testing.T) {
	t.Parallel()
	live := LiveScopes{"demo": {"api": true}, "shop": {}}
	in := []db.NotificationEvent{
		{ID: 1, Project: "demo", Service: "api"},
		{ID: 2, Project: "demo", Service: "stucktest"},  // deleted service
		{ID: 3, Project: "mkt-hedgedoc", Service: "web"}, // deleted project
		{ID: 4, Project: "shop"},                         // project-level (addon) event
		{ID: 5},                                          // platform event
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
