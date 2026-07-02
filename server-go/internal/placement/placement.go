// Package placement encodes the rules for matching kuso workloads
// against cluster nodes. The wire shape (KusoPlacement) lives in the
// kube package because it's part of the CRD; the *rules* live here
// so the kube package stays a thin types-and-client layer instead
// of accumulating domain logic.
//
// Two operations: Matches (does this node satisfy the placement?)
// and CountMatches (how many of these nodes satisfy it?). Both
// AND across labels, AND against the optional Nodes whitelist.
package placement

import (
	"kuso/server/internal/kube"
)

// NodeIdentity is the minimum projection of a Node we need for
// matching: name + the kuso label subset already filtered at the
// source. Callers fetch a snapshot from the kube informer cache
// (cheap; already kept warm) and pass it in.
type NodeIdentity struct {
	Name   string
	Labels map[string]string
}

// Matches is the canonical placement check.
//
//   - nil placement: matches every node (the "schedule anywhere"
//     default).
//   - Labels: every requested label key must be present on the node,
//     after the kuso.sislelabs.com/ prefix is applied (the user types
//     `role: web`, the node label is `kuso.sislelabs.com/role=web`).
//     A non-empty requested value must match the node's value exactly.
//     An EMPTY requested value is a presence-only check (a capability
//     flag like `gpu`): the key must exist on the node but its value
//     is irrelevant — this pairs with the nodeAffinity Exists operator
//     the helm charts render for empty-value labels.
//   - Nodes: if non-empty, the node's hostname must appear in the
//     list. Labels and Nodes AND together — a node must satisfy
//     both to match.
func Matches(p *kube.KusoPlacement, nodeName string, nodeLabels map[string]string) bool {
	if p == nil {
		return true
	}
	for k, v := range p.Labels {
		got, ok := nodeLabels["kuso.sislelabs.com/"+k]
		if !ok {
			return false
		}
		// Empty requested value = presence-only (capability flag): key
		// must exist, value is irrelevant. Non-empty = exact match.
		if v != "" && got != v {
			return false
		}
	}
	if len(p.Nodes) > 0 {
		hit := false
		for _, n := range p.Nodes {
			if n == nodeName {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// CountMatches returns how many of the supplied nodes satisfy `p`.
// 0 means the placement is unsatisfiable against this snapshot —
// every workload pod created with it will sit Pending. Callers
// should fail validation in that case so users see "no node
// matches" at save time rather than discovering an indefinitely-
// pending pod the next day.
//
// Empty / nil placement matches every node by definition.
func CountMatches(p *kube.KusoPlacement, nodes []NodeIdentity) int {
	if p == nil {
		return len(nodes)
	}
	n := 0
	for _, nd := range nodes {
		if Matches(p, nd.Name, nd.Labels) {
			n++
		}
	}
	return n
}
