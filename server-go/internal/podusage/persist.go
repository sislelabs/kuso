package podusage

import (
	"context"
	"log/slog"
	"time"

	"kuso/server/internal/db"
)

const (
	// BucketInterval is the persisted resolution.
	BucketInterval = 5 * time.Minute
	// PersistRetention is how long buckets are kept in Postgres: what
	// Load and the usage readers use (Retention) plus one prune period.
	PersistRetention = Retention + time.Hour
)

// Persister writes each completed five-minute bucket to Postgres and
// loads the last Retention back into the Store on boot.
type Persister struct {
	DB     *db.DB
	Store  *Store
	Logger *slog.Logger
}

// Load backfills the Store from Postgres. Call it before the Sampler
// starts so restored history sits ahead of the live samples.
func (p *Persister) Load(ctx context.Context, now time.Time) error {
	rows, err := p.DB.ListServiceUsageSince(ctx, now.Add(-Retention))
	if err != nil {
		return err
	}
	byKey := map[Key][]Sample{}
	for _, r := range rows {
		k := Key{Project: r.Project, Service: r.Service, Env: r.Env}
		byKey[k] = append(byKey[k], Sample{T: r.Ts.UnixMilli(), MemBytes: r.MemBytes, CPUMilli: r.CPUMilli, Pods: r.Pods})
	}
	p.Store.Backfill(byKey)
	return nil
}

// Run saves the previous bucket every BucketInterval and prunes old
// rows once an hour, until ctx is cancelled.
func (p *Persister) Run(ctx context.Context) {
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	// Tick just after each bucket boundary so the bucket is complete.
	next := time.Now().Truncate(BucketInterval).Add(BucketInterval + 10*time.Second)
	lastPrune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		now := time.Now()
		end := now.Truncate(BucketInterval)
		if err := p.save(ctx, end.Add(-BucketInterval), end); err != nil {
			p.Logger.Warn("podusage: saving history failed", "err", err)
		}
		if now.Sub(lastPrune) >= time.Hour {
			if _, err := p.DB.PruneServiceUsageOlderThan(ctx, now.Add(-PersistRetention)); err != nil {
				p.Logger.Warn("podusage: pruning history failed", "err", err)
			}
			lastPrune = now
		}
		next = end.Add(BucketInterval + 10*time.Second)
	}
}

func (p *Persister) save(ctx context.Context, from, to time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows := Buckets(p.Store.Window(from.UnixMilli(), to.UnixMilli()), from)
	if len(rows) == 0 {
		return nil
	}
	return p.DB.SaveServiceUsage(ctx, rows)
}

// Buckets folds each series' samples into one row stamped at start:
// average CPU, peak memory and pods, as History does when it
// downsamples.
func Buckets(window map[Key][]Sample, start time.Time) []db.ServiceUsage {
	out := make([]db.ServiceUsage, 0, len(window))
	for k, ss := range window {
		if len(ss) == 0 {
			continue
		}
		var cpu int64
		r := db.ServiceUsage{Project: k.Project, Service: k.Service, Env: k.Env, Ts: start.UTC()}
		for _, v := range ss {
			cpu += int64(v.CPUMilli)
			r.MemBytes = max(r.MemBytes, v.MemBytes)
			r.Pods = max(r.Pods, v.Pods)
		}
		r.CPUMilli = int32(cpu / int64(len(ss)))
		out = append(out, r)
	}
	return out
}
