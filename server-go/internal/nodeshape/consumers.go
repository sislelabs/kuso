package nodeshape

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

// Consumer is one owner's share of a node resource: a kuso service,
// addon, cron or build, or a non-kuso workload grouped by namespace.
type Consumer struct {
	// Kind is service | addon | cron | build | platform | system, or
	// host for node processes outside pods (k3s, container runtime).
	Kind      string `json:"kind"`
	Project   string `json:"project,omitempty"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Pods      int    `json:"pods"`
	Value     uint64 `json:"value"`
}

// ResourceConsumers ranks the owners of one resource. Unattributed is
// the node total minus every pod: kernel, k3s/containerd, page cache
// outside cgroups, and for disk the volumes and logs the kubelet can't
// attribute.
type ResourceConsumers struct {
	Used         uint64     `json:"used"`
	Consumers    []Consumer `json:"consumers"`
	Unattributed uint64     `json:"unattributed"`
}

// DiskConsumers adds the image store, which on k3s shares the root
// filesystem and is often its largest user.
type DiskConsumers struct {
	ResourceConsumers
	Capacity uint64 `json:"capacity"`
	Images   uint64 `json:"images"`
}

// NodeConsumers is the per-node "what is using it" breakdown. CPU is
// in millicores, memory and disk in bytes.
type NodeConsumers struct {
	CPU    ResourceConsumers `json:"cpu"`
	Memory ResourceConsumers `json:"memory"`
	Disk   DiskConsumers     `json:"disk"`
}

// BuildConsumers groups per-pod kubelet usage by owner. pod resolves a
// summary pod to its object for labels; a nil result (pod gone, cache
// cold) still counts, grouped by namespace and trimmed pod name.
func BuildConsumers(s kube.NodeUsageSummary, pod func(namespace, name string) *corev1.Pod) NodeConsumers {
	type acc struct {
		Consumer
		cpu, mem, disk uint64
	}
	groups := map[string]*acc{}
	var attrCPU, attrMem, attrDisk uint64
	for _, u := range s.Pods {
		c := ownerOf(u.Namespace, u.Name, pod(u.Namespace, u.Name))
		key := c.Kind + "\x00" + c.Namespace + "\x00" + c.Project + "\x00" + c.Name
		g, ok := groups[key]
		if !ok {
			g = &acc{Consumer: c}
			groups[key] = g
		}
		g.Pods++
		cpu := u.CPUNanoCores / 1_000_000
		g.cpu += cpu
		g.mem += u.MemWorkingSetBytes
		g.disk += u.EphemeralBytes
		attrCPU += cpu
		attrMem += u.MemWorkingSetBytes
		attrDisk += u.EphemeralBytes
	}
	rank := func(val func(*acc) uint64) []Consumer {
		out := []Consumer{}
		for _, g := range groups {
			if v := val(g); v > 0 {
				c := g.Consumer
				c.Value = v
				out = append(out, c)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Value != out[j].Value {
				return out[i].Value > out[j].Value
			}
			return out[i].Name < out[j].Name
		})
		return out
	}
	for _, h := range s.Host {
		c := Consumer{Kind: "host", Name: h.Name, Namespace: "node"}
		if h.Name == "kubelet" {
			c.Name = "k3s / kubelet + containerd"
		}
		cpu := h.CPUNanoCores / 1_000_000
		groups["host\x00"+h.Name] = &acc{Consumer: c, cpu: cpu, mem: h.MemWorkingSetBytes}
		attrCPU += cpu
		attrMem += h.MemWorkingSetBytes
	}
	nodeCPU := s.CPUNanoCores / 1_000_000
	var images uint64
	if s.ImageFSShared {
		images = s.ImageFSUsedBytes
	}
	return NodeConsumers{
		CPU: ResourceConsumers{
			Used:         nodeCPU,
			Consumers:    rank(func(g *acc) uint64 { return g.cpu }),
			Unattributed: sub(nodeCPU, attrCPU),
		},
		Memory: ResourceConsumers{
			Used:         s.MemWorkingSetBytes,
			Consumers:    rank(func(g *acc) uint64 { return g.mem }),
			Unattributed: sub(s.MemWorkingSetBytes, attrMem),
		},
		Disk: DiskConsumers{
			ResourceConsumers: ResourceConsumers{
				Used:         s.FSUsedBytes,
				Consumers:    rank(func(g *acc) uint64 { return g.disk }),
				Unattributed: sub(s.FSUsedBytes, attrDisk+images),
			},
			Capacity: s.FSCapacityBytes,
			Images:   images,
		},
	}
}

func sub(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}

func ownerOf(namespace, name string, p *corev1.Pod) Consumer {
	c := Consumer{Namespace: namespace}
	if p == nil {
		c.Kind = "system"
		c.Name = trimPodSuffix(name)
		return c
	}
	l := p.Labels
	c.Project = l[kube.LabelProject]
	switch {
	case l[kube.LabelPrefix+"vulnscan-id"] != "":
		c.Kind, c.Name = "platform", "vulnerability scans"
	case c.Project != "" && l[kube.LabelPrefix+"addon"] != "":
		c.Kind, c.Name = "addon", l[kube.LabelPrefix+"addon"]
	case c.Project != "" && l[kube.LabelPrefix+"build-ref"] != "":
		c.Kind, c.Name = "build", l[kube.LabelService]
	case c.Project != "" && l[kube.LabelPrefix+"cron"] != "":
		c.Kind, c.Name = "cron", l[kube.LabelPrefix+"cron"]
	case c.Project != "" && l[kube.LabelService] != "":
		c.Kind, c.Name = "service", l[kube.LabelService]
	default:
		c.Project = ""
		c.Kind, c.Name = "system", workloadName(p)
	}
	return c
}

// workloadName names a non-kuso pod after its controller, so the
// replicas of a Deployment are one row.
func workloadName(p *corev1.Pod) string {
	for _, o := range p.OwnerReferences {
		if o.Controller == nil || !*o.Controller {
			continue
		}
		if o.Kind == "ReplicaSet" {
			return cutLast(o.Name) // <deployment>-<template-hash>
		}
		return o.Name
	}
	return trimPodSuffix(p.Name)
}

// trimPodSuffix guesses the workload from a bare pod name:
// "coredns-6799fbcd5-x2k9q" → "coredns", "web-0" → "web".
func trimPodSuffix(name string) string {
	name = cutLast(name)
	if i := strings.LastIndexByte(name, '-'); i > 0 && looksLikeHash(name[i+1:]) {
		return name[:i]
	}
	return name
}

func cutLast(name string) string {
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		return name[:i]
	}
	return name
}

// looksLikeHash matches a ReplicaSet template hash: 6-10 lowercase
// alphanumerics with at least one digit.
func looksLikeHash(s string) bool {
	if len(s) < 6 || len(s) > 10 {
		return false
	}
	digit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z':
		default:
			return false
		}
	}
	return digit
}
