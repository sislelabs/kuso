package alerts

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"kuso/server/internal/db"
	"kuso/server/internal/notify"
)

// unreachableDB is a *db.DB whose every query fails to connect.
func unreachableDB(t *testing.T) *db.DB {
	t.Helper()
	sqldb, err := sql.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	return &db.DB{DB: sqldb}
}

// A failed episode write must not notify: the next tick re-reads the
// stale row and would otherwise send the same fire/resolve again.
func TestEvalEpisodeNoNotifyWhenPersistFails(t *testing.T) {
	t.Parallel()
	since := time.Now().Add(-time.Hour)
	cases := map[string]struct {
		rule db.AlertRule
		f    finding
	}{
		"fire": {
			rule: db.AlertRule{ID: "r", Name: "5xx", Kind: db.AlertKindHTTP5xxRate},
			f:    finding{targets: []string{"shop/web"}, details: map[string]string{"shop/web": "x"}},
		},
		"resolve": {
			rule: db.AlertRule{ID: "r", Name: "5xx", Kind: db.AlertKindHTTP5xxRate, FiringSince: &since, FiringTargets: []string{"shop/web"}},
			f:    finding{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var sent []notify.Event
			e := &Engine{DB: unreachableDB(t), Logger: slogDiscard()}
			e.emitFn = func(ev notify.Event) { sent = append(sent, ev) }
			e.episodicFn = func(context.Context, *db.AlertRule, time.Time) (finding, error) { return tc.f, nil }
			r := tc.rule
			e.evalEpisode(context.Background(), &r, time.Now())
			if len(sent) != 0 {
				t.Fatalf("sent %d events with the episode write failing, want 0", len(sent))
			}
		})
	}
}
