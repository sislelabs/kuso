package addons

// publictcp.go owns the opt-in public TCP endpoint for an addon:
// reads the cluster's configured port pool, allocates the next free
// port, and stamps spec.publicTCP.{enabled,port} on the addon CR so
// the helm chart can render the matching Traefik IngressRouteTCP.
//
// The pool is configured via the KUSO_TCP_PROXY_PORTS env var on the
// kuso-server Deployment (a range like "30000-30019"). When unset,
// EnablePublicTCP fails with a clear "not configured" error and the
// admin knows to widen the Traefik install. Free-port detection scans
// every KusoAddon cluster-wide for an existing spec.publicTCP.port —
// no separate database table; the CR itself is the source of truth.
// Allocation contention is negligible (admins click a toggle) and
// guarded by a per-process mutex.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"kuso/server/internal/kube"
)

// publicTCPMu serialises allocator runs so two concurrent EnablePublicTCP
// calls within ONE kuso-server replica can't both hand out the same port.
// It is NOT a cluster-wide lock: two DIFFERENT replicas each allocating
// for a different addon can both pass the initial scan and pick the same
// free port, because the CR patch is keyed per-addon (the addon-CR patch
// CAS detects concurrent writes to the SAME addon, not to the shared port
// pool). Cross-replica double-allocation is closed by re-scanning the
// in-use set inside the addon's UpdateKusoAddonWithRetry mutate closure
// (see EnablePublicTCP): the loser of a race sees the winner's port
// already taken and advances to the next free one instead of colliding.
var publicTCPMu sync.Mutex

// PublicTCPPool returns the inclusive [lo, hi] range configured for
// the cluster. (0, 0, false) when the env var is unset / malformed —
// callers MUST surface that as "TCP proxy not configured on this
// cluster" rather than allocating off a fallback.
func PublicTCPPool() (lo, hi int32, ok bool) {
	raw := strings.TrimSpace(os.Getenv("KUSO_TCP_PROXY_PORTS"))
	if raw == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(raw, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	b, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || a <= 0 || b <= 0 || a > 65535 || b > 65535 || a > b {
		return 0, 0, false
	}
	return int32(a), int32(b), true
}

// ErrPublicTCPNotConfigured is returned when KUSO_TCP_PROXY_PORTS is
// unset/malformed — the operator hasn't reconfigured Traefik to host
// the entrypoint pool. Wraps ErrInvalid so the HTTP layer returns 400.
var ErrPublicTCPNotConfigured = fmt.Errorf("%w: public TCP proxy is not configured on this cluster (KUSO_TCP_PROXY_PORTS unset)", ErrInvalid)

// ErrPublicTCPPoolExhausted is returned when every port in the pool
// is already allocated.
var ErrPublicTCPPoolExhausted = fmt.Errorf("%w: every port in the public TCP pool is allocated; widen KUSO_TCP_PROXY_PORTS", ErrConflict)

// EnablePublicTCP flips spec.publicTCP.{enabled:true,port:<allocated>}
// on the addon, allocating the next free port from the configured
// pool. Idempotent: if the addon already has an allocated port, that
// port is returned unchanged.
func (s *Service) EnablePublicTCP(ctx context.Context, project, name string) (int32, error) {
	lo, hi, ok := PublicTCPPool()
	if !ok {
		return 0, ErrPublicTCPNotConfigured
	}

	publicTCPMu.Lock()
	defer publicTCPMu.Unlock()

	// Idempotent: read the addon first; if already enabled with a
	// port, return that port and skip the allocator entirely.
	ns := s.nsFor(ctx, project)
	fqn := addonCRName(project, name)
	cur, err := s.Kube.GetKusoAddon(ctx, ns, fqn)
	if err != nil {
		return 0, fmt.Errorf("get addon: %w", err)
	}
	if cur.Spec.PublicTCP != nil && cur.Spec.PublicTCP.Enabled && cur.Spec.PublicTCP.Port > 0 {
		return cur.Spec.PublicTCP.Port, nil
	}

	// Build the in-use port set from every addon cluster-wide.
	inUse, err := s.usedPublicTCPPorts(ctx)
	if err != nil {
		return 0, fmt.Errorf("scan in-use ports: %w", err)
	}
	picked := int32(0)
	for p := lo; p <= hi; p++ {
		if !inUse[p] {
			picked = p
			break
		}
	}
	if picked == 0 {
		return 0, ErrPublicTCPPoolExhausted
	}

	updated, err := s.Kube.UpdateKusoAddonWithRetry(ctx, ns, fqn, func(a *kube.KusoAddon) error {
		// Re-check inside the retry loop: a concurrent EnablePublicTCP
		// for a DIFFERENT addon could have grabbed our port between
		// the scan and the patch. The mutex covers single-replica
		// kuso-server; multi-replica relies on this re-scan.
		if a.Spec.PublicTCP != nil && a.Spec.PublicTCP.Enabled && a.Spec.PublicTCP.Port > 0 {
			picked = a.Spec.PublicTCP.Port
			return nil
		}
		// Re-scan the cluster-wide in-use set from inside the mutate
		// closure (which re-runs on every 409 retry). The per-process
		// mutex only serialises allocator runs within ONE replica; two
		// replicas each allocating for a DIFFERENT addon can both pass
		// the outer scan and pick the same free port, because the CR
		// patch is keyed per-addon — it is NOT a shared lock across the
		// pool. Re-verifying here means a lost race sees the winner's
		// port already taken and advances to the next free one, so we
		// retry-and-advance instead of double-allocating.
		fresh, scanErr := s.usedPublicTCPPorts(ctx)
		if scanErr != nil {
			return fmt.Errorf("re-scan in-use ports: %w", scanErr)
		}
		if fresh[picked] {
			next := int32(0)
			for p := lo; p <= hi; p++ {
				if !fresh[p] {
					next = p
					break
				}
			}
			if next == 0 {
				return ErrPublicTCPPoolExhausted
			}
			picked = next
		}
		a.Spec.PublicTCP = &kube.KusoAddonPublicTCP{Enabled: true, Port: picked}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("patch addon: %w", err)
	}
	_ = updated
	return picked, nil
}

// DisablePublicTCP clears the public-TCP block on the addon, freeing
// its port back to the pool. Idempotent: a no-op when already off.
func (s *Service) DisablePublicTCP(ctx context.Context, project, name string) error {
	ns := s.nsFor(ctx, project)
	fqn := addonCRName(project, name)
	_, err := s.Kube.UpdateKusoAddonWithRetry(ctx, ns, fqn, func(a *kube.KusoAddon) error {
		// Clear both fields so the chart unrenders the IngressRouteTCP
		// AND the next EnablePublicTCP allocates fresh (a stale port
		// left behind would otherwise sit in usedPublicTCPPorts).
		a.Spec.PublicTCP = nil
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("patch addon: %w", err)
	}
	return nil
}

// usedPublicTCPPorts returns the set of ports already allocated to
// addons cluster-wide. A multi-tenant deployment would scope this to
// the caller's namespace; kuso is single-tenant so a global scan is
// correct AND the allocator must avoid the *whole* cluster's range.
func (s *Service) usedPublicTCPPorts(ctx context.Context) (map[int32]bool, error) {
	addons, err := s.Kube.ListKusoAddonsByLabels(ctx, "", nil)
	if err != nil {
		return nil, err
	}
	used := map[int32]bool{}
	for i := range addons {
		pt := addons[i].Spec.PublicTCP
		if pt != nil && pt.Port > 0 {
			used[pt.Port] = true
		}
	}
	return used, nil
}
