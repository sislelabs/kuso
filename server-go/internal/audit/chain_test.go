package audit

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"kuso/server/internal/notify"
)

func mustVerify(t *testing.T, s *Service) *VerifyReport {
	t.Helper()
	rep, err := s.Verify(context.Background(), VerifyOptions{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("Verify not ok: %+v", rep)
	}
	return rep
}

func chainBreak(t *testing.T, s *Service, chain string) *ChainBreak {
	t.Helper()
	rep, err := s.Verify(context.Background(), VerifyOptions{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, c := range rep.Chains {
		if c.Chain == chain {
			if c.Break == nil || rep.OK {
				t.Fatalf("%s chain: no break reported (ok=%v): %+v", chain, rep.OK, c)
			}
			return c.Break
		}
	}
	t.Fatalf("chain %s missing from report", chain)
	return nil
}

func logN(s *Service, n int, sev string) {
	for i := 0; i < n; i++ {
		s.Log(context.Background(), Entry{Severity: sev, Action: "x", Pipeline: "p", Message: fmt.Sprintf("m%d", i)})
	}
}

func TestChainLinearUnderConcurrentInserts(t *testing.T) {
	d := openAuditTestDB(t)
	s := &Service{DB: d, Enabled: true}
	const workers, each = 16, 30
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				sev := "info"
				if (w+i)%3 == 0 {
					sev = "warn"
				}
				s.Log(context.Background(), Entry{Severity: sev, Action: "x", Message: fmt.Sprintf("w%d-%d", w, i)})
			}
		}(w)
	}
	wg.Wait()

	var unchained int
	if err := d.DB.QueryRow(`SELECT count(*) FROM "Audit" WHERE "chainSeq" IS NULL`).Scan(&unchained); err != nil {
		t.Fatal(err)
	}
	if unchained != 0 {
		t.Fatalf("%d rows fell back to unchained inserts under concurrency", unchained)
	}
	rep := mustVerify(t, s)
	var rows, heads int64
	for _, c := range rep.Chains {
		rows += c.Rows
		heads += c.HeadSeq
	}
	if rows != workers*each || heads != workers*each {
		t.Fatalf("rows=%d heads=%d, want %d each", rows, heads, workers*each)
	}
}

func TestVerifyDetectsEditedRow(t *testing.T) {
	d := openAuditTestDB(t)
	s := &Service{DB: d, Enabled: true}
	logN(s, 10, "info")
	mustVerify(t, s)
	if _, err := d.DB.Exec(`UPDATE "Audit" SET message = 'nothing to see' WHERE "chain" = 'routine' AND "chainSeq" = 5`); err != nil {
		t.Fatal(err)
	}
	b := chainBreak(t, s, ChainRoutine)
	if b.Seq != 5 || !strings.Contains(b.Reason, "content changed") || b.Expected == b.Actual {
		t.Fatalf("break = %+v, want content change at seq 5", b)
	}
}

func TestVerifyDetectsEditedSeverity(t *testing.T) {
	d := openAuditTestDB(t)
	s := &Service{DB: d, Enabled: true}
	logN(s, 4, "critical")
	if _, err := d.DB.Exec(`UPDATE "Audit" SET severity = 'warn' WHERE "chain" = 'security' AND "chainSeq" = 2`); err != nil {
		t.Fatal(err)
	}
	if b := chainBreak(t, s, ChainSecurity); b.Seq != 2 {
		t.Fatalf("break = %+v, want seq 2", b)
	}
}

func TestVerifyDetectsDeletedRows(t *testing.T) {
	for _, tc := range []struct {
		name    string
		del     int64
		wantSeq int64
	}{
		{"middle", 5, 5},
		{"oldest", 1, 1},
		{"newest", 10, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openAuditTestDB(t)
			s := &Service{DB: d, Enabled: true}
			logN(s, 10, "warn")
			if _, err := d.DB.Exec(`DELETE FROM "Audit" WHERE "chain" = 'security' AND "chainSeq" = $1`, tc.del); err != nil {
				t.Fatal(err)
			}
			if b := chainBreak(t, s, ChainSecurity); b.Seq != tc.wantSeq {
				t.Fatalf("break = %+v, want seq %d", b, tc.wantSeq)
			}
		})
	}
}

// Trim keeps the chains verifiable: the age prune and the count cap each
// cut a prefix and move floorSeq with it.
func TestTrimThenVerify(t *testing.T) {
	d := openAuditTestDB(t)
	s := &Service{DB: d, Enabled: true, MaxBackups: 4, MaxAge: 90 * day, SecurityMaxAge: 365 * day}
	s.now = func() time.Time { return time.Now().Add(-400 * day) }
	logN(s, 3, "critical")
	logN(s, 3, "info")
	s.now = func() time.Time { return time.Now().Add(-100 * day) }
	logN(s, 2, "warn")
	logN(s, 3, "info")
	s.now = nil
	logN(s, 2, "error")
	logN(s, 6, "info")

	if err := s.trim(context.Background()); err != nil {
		t.Fatal(err)
	}
	rep := mustVerify(t, s)
	want := map[string][3]int64{ // floor, head, rows
		ChainSecurity: {4, 7, 4},
		ChainRoutine:  {9, 12, 4},
	}
	for _, c := range rep.Chains {
		w := want[c.Chain]
		if c.FloorSeq != w[0] || c.HeadSeq != w[1] || c.Rows != w[2] {
			t.Errorf("%s: floor=%d head=%d rows=%d, want %v", c.Chain, c.FloorSeq, c.HeadSeq, c.Rows, w)
		}
	}
	// New rows after a trim still extend the chain.
	logN(s, 2, "info")
	mustVerify(t, s)
	// Deleting below the trim's own floor is still a break.
	if _, err := d.DB.Exec(`DELETE FROM "Audit" WHERE "chain" = 'routine' AND "chainSeq" = 9`); err != nil {
		t.Fatal(err)
	}
	if b := chainBreak(t, s, ChainRoutine); b.Seq != 9 {
		t.Fatalf("break = %+v, want seq 9", b)
	}
}

func TestTrimEverythingThenVerify(t *testing.T) {
	d := openAuditTestDB(t)
	s := &Service{DB: d, Enabled: true, MaxAge: 90 * day}
	s.now = func() time.Time { return time.Now().Add(-100 * day) }
	logN(s, 3, "info")
	s.now = nil
	if err := s.trim(context.Background()); err != nil {
		t.Fatal(err)
	}
	rep := mustVerify(t, s)
	if c := rep.Chains[1]; c.Rows != 0 || c.FloorSeq != 4 {
		t.Fatalf("routine = %+v, want empty with floor 4", c)
	}
}

func TestUnchainedRowsReported(t *testing.T) {
	d := openAuditTestDB(t)
	s := &Service{DB: d, Enabled: true}
	logN(s, 2, "info")
	if _, err := d.DB.Exec(`
INSERT INTO "Audit" (timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user")
VALUES (now(), 'info', 'forged', '', '', '', '', 'unknown', 'injected', '1')`); err != nil {
		t.Fatal(err)
	}
	rep := mustVerify(t, s)
	if rep.UnchainedSinceStart != 1 {
		t.Fatalf("UnchainedSinceStart = %d, want 1", rep.UnchainedSinceStart)
	}
}

func TestRowHashFieldBoundaries(t *testing.T) {
	a := chainRecord{Chain: ChainRoutine, Seq: 1, Action: "ab", Namespace: "c"}
	b := chainRecord{Chain: ChainRoutine, Seq: 1, Action: "a", Namespace: "bc"}
	if hex.EncodeToString(rowHash(genesisHash, a)) == hex.EncodeToString(rowHash(genesisHash, b)) {
		t.Fatal("moving bytes between fields must change the hash")
	}
}

type fakeEmitter struct {
	events []notify.Event
	err    error
}

func (f *fakeEmitter) EmitDurable(e notify.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func TestCheckpointOnlyWhenHeadMoved(t *testing.T) {
	d := openAuditTestDB(t)
	clock := time.Now()
	s := &Service{DB: d, Enabled: true, now: func() time.Time { return clock }}
	em := &fakeEmitter{}
	c := &Checkpointer{Svc: s, Notify: em, Interval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()
	step := func(want bool, label string) {
		t.Helper()
		got, err := c.Checkpoint(ctx)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got != want {
			t.Fatalf("%s: emitted=%v, want %v", label, got, want)
		}
	}

	step(false, "empty chains")
	logN(s, 2, "warn")
	logN(s, 1, "info")
	step(true, "first rows")
	ev := em.events[0]
	if ev.Type != notify.EventAuditCheckpoint || ev.Audience != notify.AudienceUnscoped ||
		ev.Extra["security.seq"] != "2" || ev.Extra["routine.seq"] != "1" {
		t.Fatalf("checkpoint event = %+v", ev)
	}
	logN(s, 1, "info")
	step(false, "inside the interval")
	clock = clock.Add(2 * time.Hour)
	step(true, "interval passed, head moved")
	clock = clock.Add(2 * time.Hour)
	step(false, "head did not move")

	logN(s, 1, "warn")
	clock = clock.Add(2 * time.Hour)
	em.err = errors.New("outbox down")
	if ok, err := c.Checkpoint(ctx); err == nil || ok {
		t.Fatalf("failed emit: ok=%v err=%v, want error", ok, err)
	}
	em.err = nil
	step(true, "retry after failed emit")

	last := em.events[len(em.events)-1]
	seq, _ := strconv.ParseInt(last.Extra["security.seq"], 10, 64)
	rep, err := s.Verify(ctx, VerifyOptions{Chain: ChainSecurity, Seq: seq, Hash: last.Extra["security.hash"]})
	if err != nil || !rep.OK || rep.Checkpoint == nil || !rep.Checkpoint.OK {
		t.Fatalf("published checkpoint does not verify: %+v err=%v", rep, err)
	}
}

// The chain hash is keyless, so someone with DB access can rewrite a row
// and recompute everything after it. Only a published checkpoint catches
// that, which is the reason checkpoints exist.
func TestCheckpointCatchesRecomputedChain(t *testing.T) {
	d := openAuditTestDB(t)
	s := &Service{DB: d, Enabled: true}
	ctx := context.Background()
	logN(s, 5, "critical")
	rep := mustVerify(t, s)
	published := rep.Chains[0]

	rows, err := d.DB.Query(`SELECT ` + chainRowCols + ` FROM "Audit" WHERE "chain" = 'security' ORDER BY "chainSeq"`)
	if err != nil {
		t.Fatal(err)
	}
	var recs []chainRecord
	for rows.Next() {
		r, _, _, err := scanChainRow(rows, ChainSecurity)
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	rows.Close()
	recs[2].Message = "rewritten"
	prev := genesisHash
	for _, r := range recs {
		h := rowHash(prev, r)
		if _, err := d.DB.Exec(`UPDATE "Audit" SET message = $1, "prevHash" = $2, "hash" = $3 WHERE "chain" = 'security' AND "chainSeq" = $4`,
			r.Message, prev, h, r.Seq); err != nil {
			t.Fatal(err)
		}
		prev = h
	}
	if _, err := d.DB.Exec(`UPDATE "AuditChainHead" SET "hash" = $1 WHERE "chain" = 'security'`, prev); err != nil {
		t.Fatal(err)
	}

	mustVerify(t, s) // self-consistent again
	rep, err = s.Verify(ctx, VerifyOptions{Chain: ChainSecurity, Seq: published.HeadSeq, Hash: published.HeadHash})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Checkpoint == nil || rep.Checkpoint.OK {
		t.Fatalf("rewritten chain passed against the published checkpoint: %+v", rep.Checkpoint)
	}
}

func TestVerifyCheckpointEdgeCases(t *testing.T) {
	d := openAuditTestDB(t)
	s := &Service{DB: d, Enabled: true, MaxBackups: 2}
	ctx := context.Background()
	logN(s, 5, "info")
	if err := s.trim(ctx); err != nil {
		t.Fatal(err)
	}
	zero := strings.Repeat("00", 32)
	rep, err := s.Verify(ctx, VerifyOptions{Chain: ChainRoutine, Seq: 1, Hash: zero})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || !strings.Contains(rep.Checkpoint.Reason, "trimmed") {
		t.Fatalf("trimmed checkpoint: %+v", rep.Checkpoint)
	}
	for _, bad := range []VerifyOptions{
		{Chain: "nope", Seq: 1, Hash: zero},
		{Chain: ChainRoutine, Seq: 0, Hash: zero},
		{Chain: ChainRoutine, Seq: 1, Hash: "abc"},
	} {
		if _, err := s.Verify(ctx, bad); !errors.Is(err, ErrBadCheckpoint) {
			t.Errorf("Verify(%+v) err = %v, want ErrBadCheckpoint", bad, err)
		}
	}
}
