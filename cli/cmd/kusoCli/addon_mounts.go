package kusoCli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// serviceAddonMount is one service's subscribed-addons read, as used by
// the post-add summary. Err is set when the read failed.
type serviceAddonMount struct {
	Service    string
	Subscribed []string
	Available  []string
	Err        error
}

// addonMountSummary renders which services mount a freshly added addon.
// The server reports an unset subscription list as "every addon", so a
// service mounts the addon iff it's in Subscribed. An addon missing from
// Available means the server hasn't seen it yet (informer lag): that
// service is reported as unconfirmed rather than guessed.
func addonMountSummary(project, addon string, mounts []serviceAddonMount) string {
	if len(mounts) == 0 {
		return fmt.Sprintf("No services in %s yet.\n", project)
	}
	var mounted, notMounted, unknown []string
	for _, m := range mounts {
		switch {
		case m.Err != nil || !contains(m.Available, addon):
			unknown = append(unknown, m.Service)
		case contains(m.Subscribed, addon):
			mounted = append(mounted, m.Service)
		default:
			notMounted = append(notMounted, m.Service)
		}
	}
	sort.Strings(mounted)
	sort.Strings(notMounted)
	sort.Strings(unknown)

	var b strings.Builder
	if len(mounted) > 0 {
		fmt.Fprintf(&b, "Mounted on: %s\n", strings.Join(mounted, ", "))
	}
	if len(notMounted) > 0 {
		fmt.Fprintf(&b, "Not mounted on: %s (explicit subscription list). To add it:\n", strings.Join(notMounted, ", "))
		for _, s := range notMounted {
			fmt.Fprintf(&b, "  kuso project addon subscribe %s %s %s\n", project, s, addon)
		}
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, "Could not confirm yet for: %s. Check with: kuso project addon list %s <service>\n",
			strings.Join(unknown, ", "), project)
	}
	return b.String()
}

// printAddonMounts lists the project's services and reads each one's
// subscriptions. Failures here never fail the add: the addon exists.
func printAddonMounts(w io.Writer, project, addon string) {
	resp, err := api.GetServices(project)
	if err := checkRespErr(resp, err); err != nil {
		fmt.Fprintf(w, "(could not list services to show where %s is mounted: %v)\n", addon, err)
		return
	}
	var items []map[string]any
	if err := json.Unmarshal(resp.Body(), &items); err != nil {
		return
	}
	services := make([]string, 0, len(items))
	for _, it := range items {
		services = append(services, stripPrefix(resourceName(it), project+"-"))
	}
	read := func() ([]serviceAddonMount, bool) {
		out := make([]serviceAddonMount, 0, len(services))
		pending := false
		for _, s := range services {
			m := serviceAddonMount{Service: s}
			sub, err := readSubscribedAddons(project, s)
			if err != nil {
				m.Err = err
			} else {
				m.Subscribed, m.Available = sub.Subscribed, sub.Available
				if !contains(sub.Available, addon) {
					pending = true
				}
			}
			out = append(out, m)
		}
		return out, pending
	}
	mounts, pending := read()
	// The server's addon list is cache-backed; a just-created addon can
	// take a moment to appear.
	for i := 0; pending && i < 3; i++ {
		time.Sleep(time.Second)
		mounts, pending = read()
	}
	fmt.Fprint(w, addonMountSummary(project, addon, mounts))
}

// addonHANote is the single-line reminder printed when a fragile kind
// is added without --ha. Empty for kinds that need no note.
func addonHANote(kind string) string {
	switch kind {
	case "nats":
		return "warning: single-pod NATS drops in-flight JetStream messages on pod loss; pass --ha for 3 replicas (docs/ADDON_HA.md)"
	case "redis":
		return "warning: single-pod Redis fails reads and writes for ~30-60s on pod loss; pass --ha for 3 Redis + 3 Sentinel (docs/ADDON_HA.md)"
	case "postgres":
		return "note: single-pod Postgres; pass --ha for 3 CloudNativePG replicas with automatic failover (docs/ADDON_HA.md)"
	}
	return ""
}
