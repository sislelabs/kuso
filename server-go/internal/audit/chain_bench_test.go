package audit

import (
	"context"
	"testing"
	"time"
)

// Run with KUSO_TEST_PG_DSN set:
//
//	go test -run '^$' -bench . ./internal/audit/
func benchEntry() Entry {
	return Entry{User: "u1", Severity: "info", Action: "addon.sql_query", Pipeline: "shop", App: "api", Resource: "addon", Message: "SELECT 1"}
}

// BenchmarkLogUnchained is the pre-chain INSERT, for comparison.
func BenchmarkLogUnchained(b *testing.B) {
	d := openAuditTestDB(b)
	ctx := context.Background()
	e := benchEntry()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now := time.Now().UTC()
		if _, err := d.ExecContext(ctx, `
INSERT INTO "Audit" (timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user", "createdAt", "updatedAt")
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			now, e.Severity, e.Action, e.Namespace, e.Phase, e.App, e.Pipeline, e.Resource, e.Message, e.User, now, now); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLogChained(b *testing.B) {
	d := openAuditTestDB(b)
	s := &Service{DB: d, Enabled: true}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.insertChained(ctx, benchEntry(), s.clock()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLogChainedParallel measures throughput with every goroutine
// contending for the one routine head row.
func BenchmarkLogChainedParallel(b *testing.B) {
	d := openAuditTestDB(b)
	s := &Service{DB: d, Enabled: true}
	ctx := context.Background()
	b.SetParallelism(4)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := s.insertChained(ctx, benchEntry(), s.clock()); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkVerify50k(b *testing.B) {
	d := openAuditTestDB(b)
	s := &Service{DB: d, Enabled: true}
	ctx := context.Background()
	for i := 0; i < 50000; i++ {
		if err := s.insertChained(ctx, benchEntry(), s.clock()); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rep, err := s.Verify(ctx, VerifyOptions{})
		if err != nil || !rep.OK {
			b.Fatalf("verify: %+v %v", rep, err)
		}
	}
}
