package releaserun

import (
	"sort"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

// PlacementScheduling renders a resolved placement the way the
// kusoenvironment chart does, so a Job running an env's code lands on the
// nodes its pods are pinned to (e.g. the only nodes that can reach a private
// DB). Value labels become a nodeSelector; empty-value labels (capability
// flags) and the hostname pin share one AND-ed nodeAffinity term; a region
// label tolerates the region taint.
func PlacementScheduling(p *kube.KusoPlacement) (map[string]string, *corev1.Affinity, []corev1.Toleration) {
	if p == nil {
		return nil, nil, nil
	}
	var nodeSelector map[string]string
	var exprs []corev1.NodeSelectorRequirement
	for k, v := range p.Labels {
		key := "kuso.sislelabs.com/" + k
		if v == "" {
			exprs = append(exprs, corev1.NodeSelectorRequirement{Key: key, Operator: corev1.NodeSelectorOpExists})
			continue
		}
		if nodeSelector == nil {
			nodeSelector = map[string]string{}
		}
		nodeSelector[key] = v
	}
	sort.Slice(exprs, func(i, j int) bool { return exprs[i].Key < exprs[j].Key })
	if len(p.Nodes) > 0 {
		exprs = append(exprs, corev1.NodeSelectorRequirement{
			Key:      "kubernetes.io/hostname",
			Operator: corev1.NodeSelectorOpIn,
			Values:   append([]string(nil), p.Nodes...),
		})
	}
	var affinity *corev1.Affinity
	if len(exprs) > 0 {
		affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: exprs}},
			},
		}}
	}
	var tolerations []corev1.Toleration
	if region := p.Labels["region"]; region != "" {
		tolerations = []corev1.Toleration{{
			Key:      "kuso.sislelabs.com/region",
			Operator: corev1.TolerationOpEqual,
			Value:    region,
			Effect:   corev1.TaintEffectNoSchedule,
		}}
	}
	return nodeSelector, affinity, tolerations
}
