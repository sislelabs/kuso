package uptime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"kuso/server/internal/notify"
)

// The through-Traefik check (ingress.go) dials the Traefik ClusterIP, so
// a broken load balancer, hostPort, firewall or DNS record in front of
// it stays invisible. KUSO_UPTIME_EXTERNAL_URL opts into one more probe
// that goes the way users do: public DNS, then the node's public
// address. Opt-in because many networks don't hairpin (a pod dialling
// its own cluster's public IP gets refused), and on those this probe
// would page forever.

type externalState struct {
	fails, oks int
	down       bool
	downAt     time.Time
}

// ExternalURL is the opt-in public-path probe target, "" when off.
func ExternalURL() string { return strings.TrimSpace(os.Getenv("KUSO_UPTIME_EXTERNAL_URL")) }

// checkExternal probes the public URL once and pages ingress.down on
// ingressFailTicks failures in a row. A fresh process starts at zero, so
// the first tick after an upgrade can't page.
func (w *Watcher) checkExternal(ctx context.Context) {
	target := w.externalURL()
	if target == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ProbeTimeout+5*time.Second)
	defer cancel()
	probe := w.ExternalProbe
	if probe == nil {
		probe = NewProber(ProbeTimeout).Probe
	}
	r := probe(ctx, target)
	st := &w.external
	now := w.Now()
	if !r.OK {
		st.oks = 0
		st.fails++
		if st.fails >= ingressFailTicks && !st.down {
			ev := notify.IngressDown("external path",
				fmt.Sprintf("%s did not answer over the public path (%d checks in a row). Last error: %s. Check the load balancer, firewall and DNS in front of Traefik.",
					target, st.fails, r.Error))
			if err := w.Notify.EmitDurable(ev); err != nil {
				w.Logger.Warn("uptime: external-path notify failed, retrying next tick", "err", err)
				return
			}
			st.down, st.downAt = true, now
		}
		return
	}
	st.fails = 0
	st.oks++
	if st.down && st.oks >= ingressOKTicks {
		if err := w.Notify.EmitDurable(notify.PlatformRecovered("ingress (external path)", now.Sub(st.downAt))); err != nil {
			w.Logger.Warn("uptime: external-path recovery notify failed, retrying next tick", "err", err)
			return
		}
		st.down = false
	}
}

func (w *Watcher) externalURL() string {
	if w.ExternalTarget != nil {
		return w.ExternalTarget()
	}
	return ExternalURL()
}
