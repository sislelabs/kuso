package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeRevStore counts DB hits and can be flipped to error to simulate a
// Postgres outage.
type fakeRevStore struct {
	revoked   bool
	watermark time.Time
	err       error
	jtiHits   int
	wmHits    int
}

func (f *fakeRevStore) IsTokenRevoked(_ context.Context, _ string) (bool, error) {
	f.jtiHits++
	if f.err != nil {
		return false, f.err
	}
	return f.revoked, nil
}

func (f *fakeRevStore) UserTokenWatermark(_ context.Context, _ string) (time.Time, error) {
	f.wmHits++
	if f.err != nil {
		return time.Time{}, f.err
	}
	return f.watermark, nil
}

func TestRevocation_ReadThroughSkipsDBWhenFresh(t *testing.T) {
	store := &fakeRevStore{revoked: false}
	check := makeRevocationChecker(store)
	ctx := context.Background()
	iat := time.Now().Add(-time.Hour)

	// First call warms the cache (one jti + one watermark query).
	if check(ctx, "jti-1", "user-1", iat) {
		t.Fatal("fresh non-revoked token reported revoked")
	}
	if store.jtiHits != 1 || store.wmHits != 1 {
		t.Fatalf("want 1 jti + 1 wm hit warming cache, got jti=%d wm=%d", store.jtiHits, store.wmHits)
	}
	// Subsequent calls within the fresh window must NOT hit the DB again —
	// this is the whole point of read-through (steady-state ~zero QPS).
	for i := 0; i < 5; i++ {
		if check(ctx, "jti-1", "user-1", iat) {
			t.Fatal("cached non-revoked token reported revoked")
		}
	}
	if store.jtiHits != 1 || store.wmHits != 1 {
		t.Errorf("read-through should skip the DB while fresh; got jti=%d wm=%d", store.jtiHits, store.wmHits)
	}
}

func TestRevocation_RevokedIsAuthoritative(t *testing.T) {
	store := &fakeRevStore{revoked: true}
	check := makeRevocationChecker(store)
	if !check(context.Background(), "jti-x", "user-1", time.Now()) {
		t.Fatal("revoked jti must report revoked")
	}
}

func TestRevocation_DBErrorNoCacheFailsClosed(t *testing.T) {
	store := &fakeRevStore{err: errors.New("db down")}
	check := makeRevocationChecker(store)
	// Never queried successfully → no cache → must fail closed (revoked).
	if !check(context.Background(), "jti-cold", "user-cold", time.Now()) {
		t.Error("cold DB error must fail closed (treat as revoked)")
	}
}

func TestRevocation_DBErrorUsesStaleFallback(t *testing.T) {
	store := &fakeRevStore{revoked: false, watermark: time.Time{}}
	check := makeRevocationChecker(store)
	ctx := context.Background()
	iat := time.Now().Add(-time.Hour)

	// Warm the cache with a good (non-revoked) answer.
	if check(ctx, "jti-warm", "user-warm", iat) {
		t.Fatal("setup: token unexpectedly revoked")
	}
	// Now the DB goes down. Within the stale window the cached "not
	// revoked" answer must keep the user authenticated rather than 401.
	store.err = errors.New("db down")
	if check(ctx, "jti-warm", "user-warm", iat) {
		t.Error("stale non-revoked cache should keep the user authed during a DB outage")
	}
}
