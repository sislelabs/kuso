package envisolation

import (
	"context"
	"log/slog"
	"net"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"

	"kuso/server/internal/kube"
)

// Syncer keeps two project spec fields equal to cluster state, so the
// project release re-renders as soon as that state changes instead of on
// the operator's 30-minute periodic reconcile:
//
//   - spec.networkPolicy.knownEnvScopes: the non-production env scopes of
//     the project's env CRs (ISO-1). The chart also reads them with a helm
//     lookup, but only at render time; without this write a new env group,
//     named env or preview ran on the flat project-wide rule for up to 30
//     minutes.
//   - spec.networkPolicy.nodeBlockCIDRs: the public IPv4 addresses of the
//     cluster's nodes (ISO-7), excepted from public egress so pods can't
//     reach node-bound services (kubelet, apiserver, etcd) over them.
//
// Both writes are derived from the informer cache on every pass, so the
// result is the same on any replica and after any restart. Run it on the
// leader only.
type Syncer struct {
	Kube   *kube.Client
	HomeNS string
	Logger *slog.Logger
	// BlockNodeIPs writes nodeBlockCIDRs. When false the field is cleared.
	BlockNodeIPs bool
	// Interval is the safety-net full pass; env events trigger one sooner.
	Interval time.Duration
}

// NodeIPBlockEnabled reads KUSO_BLOCK_NODE_IPS; off unless explicitly on.
// Blocking node public IPs is only safe where a pod calling its own public
// URL is DNAT'd to traefik before policy evaluation, which isn't verified
// on every install, so it stays opt-in.
func NodeIPBlockEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KUSO_BLOCK_NODE_IPS"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

func (s *Syncer) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	kick := make(chan struct{}, 1)
	signal := func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	if s.Kube.Cache != nil {
		if inf := s.Kube.Cache.CRDInformer(kube.GVREnvironments); inf != nil {
			reg, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(any) { signal() },
				UpdateFunc: func(oldObj, newObj any) {
					if envScopeOf(oldObj) != envScopeOf(newObj) {
						signal()
					}
				},
				DeleteFunc: func(any) { signal() },
			})
			if err == nil {
				defer func() { _ = inf.RemoveEventHandler(reg) }()
			} else {
				s.Logger.Warn("envisolation sync: AddEventHandler", "err", err)
			}
		}
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	s.SyncAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-kick:
			// Coalesce a burst (an env group creates one env per service).
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			select {
			case <-kick:
			default:
			}
			s.SyncAll(ctx)
		case <-t.C:
			s.SyncAll(ctx)
		}
	}
}

func envScopeOf(obj any) string {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		v, _, _ := unstructured.NestedString(u.Object, "spec", "envScope")
		return v
	}
	return ""
}

// SyncAll runs one pass over every project. Errors are logged per project;
// the next pass retries.
func (s *Syncer) SyncAll(ctx context.Context) {
	projects, err := s.Kube.ListKusoProjects(ctx, s.HomeNS)
	if err != nil {
		s.Logger.Warn("envisolation sync: list projects", "err", err)
		return
	}
	nodeCIDRs, nodesOK := []string(nil), true
	if s.BlockNodeIPs {
		nodeCIDRs, nodesOK = s.nodeCIDRs(ctx)
	}
	for i := range projects {
		p := &projects[i]
		if p.Spec.NetworkPolicy != nil && p.Spec.NetworkPolicy.Enabled != nil && !*p.Spec.NetworkPolicy.Enabled {
			continue
		}
		var scopes []string
		wantScopes := nonProdIsolated(p)
		if wantScopes {
			ns := p.Spec.Namespace
			if ns == "" {
				ns = s.HomeNS
			}
			envs, err := s.Kube.ListKusoEnvironments(ctx, ns)
			if err != nil {
				s.Logger.Warn("envisolation sync: list envs", "project", p.Name, "err", err)
				wantScopes = false
			} else {
				scopes = EnvScopes(p.Name, envs)
			}
		}
		cur := p.Spec.NetworkPolicy
		scopesChanged := wantScopes && (cur == nil || !slices.Equal(cur.KnownEnvScopes, scopes))
		nodesChanged := nodesOK && !slices.Equal(curNodeCIDRs(cur), nodeCIDRs)
		if !scopesChanged && !nodesChanged {
			continue
		}
		_, err := s.Kube.UpdateKusoProjectWithRetry(ctx, s.HomeNS, p.Name, func(live *kube.KusoProject) error {
			if live.Spec.NetworkPolicy == nil {
				live.Spec.NetworkPolicy = &kube.KusoProjectNetworkPolicy{}
			}
			if scopesChanged {
				live.Spec.NetworkPolicy.KnownEnvScopes = scopes
			}
			if nodesChanged {
				live.Spec.NetworkPolicy.NodeBlockCIDRs = nodeCIDRs
			}
			return nil
		})
		if err != nil {
			s.Logger.Warn("envisolation sync: update project", "project", p.Name, "err", err)
			continue
		}
		s.Logger.Info("envisolation sync: project re-rendered", "project", p.Name,
			"knownEnvScopes", scopes, "scopesChanged", scopesChanged, "nodeBlockCIDRs", nodeCIDRs, "nodesChanged", nodesChanged)
	}
}

func curNodeCIDRs(np *kube.KusoProjectNetworkPolicy) []string {
	if np == nil {
		return nil
	}
	return np.NodeBlockCIDRs
}

// EnvScopes returns the sorted non-production spec.envScope values of
// project's env CRs, the same set the chart's lookup builds. Envs being
// deleted are left out so their policy goes with them.
func EnvScopes(project string, envs []kube.KusoEnvironment) []string {
	set := map[string]bool{}
	for i := range envs {
		e := &envs[i]
		if e.Spec.Project != project || e.DeletionTimestamp != nil {
			continue
		}
		if sc := e.Spec.EnvScope; sc != "" && sc != "production" {
			set[sc] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for sc := range set {
		out = append(out, sc)
	}
	sort.Strings(out)
	return out
}

func (s *Syncer) nodeCIDRs(ctx context.Context) ([]string, bool) {
	if nodes, ok := s.Kube.Cache.ListNodes(); ok {
		return PublicNodeCIDRs(nodes), true
	}
	list, err := s.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		s.Logger.Warn("envisolation sync: list nodes", "err", err)
		return nil, false
	}
	nodes := make([]*corev1.Node, 0, len(list.Items))
	for i := range list.Items {
		nodes = append(nodes, &list.Items[i])
	}
	return PublicNodeCIDRs(nodes), true
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// PublicNodeCIDRs returns a /32 for every public IPv4 InternalIP or
// ExternalIP of nodes, sorted. Private, loopback, link-local and CGNAT
// addresses are already in the chart's except list; IPv6 is skipped
// because the except list sits under the 0.0.0.0/0 block.
//
// Excepting a node IP does not break the ingress hairpin: kube-proxy
// DNATs a pod's connection to the Traefik LoadBalancer IP to a Traefik
// pod before NetworkPolicy sees it, and that path has its own rule.
func PublicNodeCIDRs(nodes []*corev1.Node) []string {
	set := map[string]bool{}
	for _, n := range nodes {
		for _, a := range n.Status.Addresses {
			if a.Type != corev1.NodeInternalIP && a.Type != corev1.NodeExternalIP {
				continue
			}
			ip := net.ParseIP(strings.TrimSpace(a.Address)).To4()
			if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
				ip.IsUnspecified() || ip.IsMulticast() || cgnat.Contains(ip) {
				continue
			}
			set[ip.String()+"/32"] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
