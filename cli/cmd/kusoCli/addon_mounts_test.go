package kusoCli

import (
	"errors"
	"strings"
	"testing"
)

func TestAddonMountSummary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mounts []serviceAddonMount
		want   string
	}{
		{
			name: "Mixed",
			mounts: []serviceAddonMount{
				{Service: "worker", Subscribed: []string{"cache", "db"}, Available: []string{"cache", "db"}},
				{Service: "api", Subscribed: []string{"cache"}, Available: []string{"cache", "db"}},
				{Service: "web", Subscribed: []string{"cache", "db"}, Available: []string{"cache", "db"}},
				{Service: "cron", Subscribed: []string{"cache"}, Available: []string{"cache"}},
				{Service: "admin", Err: errors.New("boom")},
			},
			want: "Mounted on: web, worker\n" +
				"Not mounted on: api (explicit subscription list). To add it:\n" +
				"  kuso project addon subscribe shop api db\n" +
				"Could not confirm yet for: admin, cron. Check with: kuso project addon list shop <service>\n",
		},
		{name: "NoServices", want: "No services in shop yet.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := addonMountSummary("shop", "db", tc.mounts); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestAddonHANoteIsOneLine(t *testing.T) {
	for _, k := range []string{"postgres", "redis", "nats"} {
		n := addonHANote(k)
		if n == "" || strings.Contains(n, "\n") {
			t.Errorf("%s: want one non-empty line, got %q", k, n)
		}
	}
	if addonHANote("mongodb") != "" {
		t.Error("mongodb should have no HA note")
	}
}
