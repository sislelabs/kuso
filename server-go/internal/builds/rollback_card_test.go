package builds

import (
	"strings"
	"testing"
)

// The rollback card used to print full build CR names and the image tag
// ("back to build `shop-api-c850e9b0ef35-mw883yzn` (image `c850…`)").
func TestEmitRolledBack_NamesBuildsByCommit(t *testing.T) {
	rec := &recordingEmitter{}
	s := &Service{Notifier: rec}
	s.emitRolledBack("shop", "api", "production", "shop-api-c850e9b0ef35-mw883yzn", "shop-api-d1e2f3a4b5c6-mx001abc", "c850e9b0ef35-mw883yzn", RollbackOptions{Actor: "admin"})
	if len(rec.events) != 1 {
		t.Fatalf("events = %d", len(rec.events))
	}
	e := rec.events[0]
	if e.Description != "admin rolled production back to c850e9b.\nPreviously live: d1e2f3a." {
		t.Errorf("description = %q", e.Description)
	}
	if len(e.Fields) != 0 {
		t.Errorf("fields = %+v; build and actor are already in the description", e.Fields)
	}
	if strings.Contains(e.URL, "?service=") || !strings.HasPrefix(e.URL, "/projects/shop/services/api") {
		t.Errorf("url = %q, want the service page", e.URL)
	}
}

func TestBuildLabel(t *testing.T) {
	for in, want := range map[string]string{
		"shop-api-c850e9b0ef35-mw883yzn": "c850e9b",
		"c850e9b0ef35-mw883yzn":          "c850e9b",
		"main-mp81chv5":                  "main-mp81chv5",
		"v1.2.3":                         "v1.2.3",
	} {
		if got := buildLabel(in); got != want {
			t.Errorf("buildLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
