package addons

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"kuso/server/internal/kube"
)

// A tenant addon on a shared instance Postgres renders nothing of its own,
// so its helm conditions always read "deployed". Its real health is the
// shared server's: reachable, and holding the tenant's database. The list
// path must not block on Postgres, so it reads a per-instance snapshot that
// is refreshed in the background.
const (
	instanceSnapRefresh = 60 * time.Second
	// A snapshot older than this is not trusted; the tenant falls back to
	// its helm conditions rather than showing a stale verdict.
	instanceSnapMaxAge = 5 * time.Minute
)

type instanceSnap struct {
	mu         sync.Mutex
	refreshing bool
	at         time.Time
	reachable  bool
	err        string
	dbs        map[string]bool
}

func (s *Service) snapFor(instance string) *instanceSnap {
	v, _ := s.instanceSnaps.LoadOrStore(instance, &instanceSnap{})
	return v.(*instanceSnap)
}

// instanceSnapshot returns the last snapshot for instance (ok=false when
// none is fresh enough) and kicks off a refresh when it is due.
func (s *Service) instanceSnapshot(instance string) (reachable bool, errText string, dbs map[string]bool, ok bool) {
	sn := s.snapFor(instance)
	sn.mu.Lock()
	defer sn.mu.Unlock()
	if !sn.refreshing && time.Since(sn.at) > instanceSnapRefresh {
		sn.refreshing = true
		go s.refreshInstanceSnap(instance, sn)
	}
	if sn.at.IsZero() || time.Since(sn.at) > instanceSnapMaxAge {
		return false, "", nil, false
	}
	return sn.reachable, sn.err, sn.dbs, true
}

func (s *Service) refreshInstanceSnap(instance string, sn *instanceSnap) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	dbs, err := s.listInstanceDBs(ctx, instance)
	sn.mu.Lock()
	defer sn.mu.Unlock()
	sn.refreshing = false
	sn.at = time.Now()
	sn.reachable = err == nil
	sn.err = ""
	sn.dbs = dbs
	if err != nil {
		sn.err = err.Error()
		slog.Default().Debug("instance health snapshot", "instance", instance, "err", err)
	}
}

// listInstanceDBs names every database on the instance server.
func (s *Service) listInstanceDBs(ctx context.Context, instance string) (map[string]bool, error) {
	dsn, err := s.instanceAdminDSN(ctx, instance)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open instance %s: %w", instance, err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT datname FROM pg_database WHERE NOT datistemplate`)
	if err != nil {
		return nil, fmt.Errorf("instance %s unreachable: %w", instance, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// instanceTenantState folds the shared server into a tenant addon's rollup.
// ok=false means "no verdict", leaving the helm conditions in charge.
func (s *Service) instanceTenantState(a *kube.KusoAddon) (state, detail string, ok bool) {
	reachable, errText, dbs, fresh := s.instanceSnapshot(a.Spec.UseInstanceAddon)
	if !fresh {
		return "", "", false
	}
	return deriveInstanceTenantState(a, reachable, errText, dbs)
}

func deriveInstanceTenantState(a *kube.KusoAddon, reachable bool, errText string, dbs map[string]bool) (string, string, bool) {
	inst := a.Spec.UseInstanceAddon
	if failed, msg, _ := helmConditions(a.Status); failed {
		if msg == "" {
			msg = "Helm release failed"
		}
		return addonStateFailing, msg, true
	}
	if !reachable {
		detail := "Shared instance database " + inst + " is unreachable"
		if errText != "" {
			detail += ": " + errText
		}
		return addonStateFailing, detail, true
	}
	p := a.Spec.Project
	if p == "" {
		p = a.Labels[kube.LabelProject]
	}
	// Either derivation counts: a pre-DATA-02 resync left some tenants on
	// the short-name database (see resolveInstanceAddonName).
	derived := pgIdentifier(p, instanceAddonName(a, p))
	if !dbs[derived] && !dbs[pgIdentifier(p, ShortName(p, a.Name))] {
		return addonStateFailing, fmt.Sprintf("Database %s does not exist on the shared instance database %s", derived, inst), true
	}
	return addonStateLive, "Runs on the shared instance database " + inst, true
}
