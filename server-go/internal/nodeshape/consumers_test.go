package nodeshape

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func TestBuildConsumers_GroupsByOwnerAndKeepsTheRemainder(t *testing.T) {
	yes := true
	pods := map[string]*corev1.Pod{
		"kuso/web-a": {ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{kube.LabelProject: "shop", kube.LabelService: "web"}}},
		"kuso/web-b": {ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{kube.LabelProject: "shop", kube.LabelService: "web"}}},
		"kuso/db-0": {ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
			kube.LabelProject: "shop", kube.LabelPrefix + "addon": "shop-db", kube.LabelService: "ignored"}}},
		"kube-system/coredns-6799fbcd5-x2k9q": {ObjectMeta: metav1.ObjectMeta{
			Name:            "coredns-6799fbcd5-x2k9q",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "coredns-6799fbcd5", Controller: &yes}},
		}},
	}
	s := kube.NodeUsageSummary{
		CPUNanoCores: 2_000_000_000, MemWorkingSetBytes: 10_000,
		FSCapacityBytes: 1_000_000, FSUsedBytes: 900_000,
		ImageFSUsedBytes: 600_000, ImageFSShared: true,
		Host: []kube.PodUsage{{Name: "kubelet", CPUNanoCores: 1_000_000_000, MemWorkingSetBytes: 2_000}},
		Pods: []kube.PodUsage{
			{Namespace: "kuso", Name: "web-a", CPUNanoCores: 300_000_000, MemWorkingSetBytes: 1_000, EphemeralBytes: 10_000},
			{Namespace: "kuso", Name: "web-b", CPUNanoCores: 200_000_000, MemWorkingSetBytes: 1_000, EphemeralBytes: 10_000},
			{Namespace: "kuso", Name: "db-0", CPUNanoCores: 100_000_000, MemWorkingSetBytes: 5_000, EphemeralBytes: 1_000},
			{Namespace: "kube-system", Name: "coredns-6799fbcd5-x2k9q", CPUNanoCores: 50_000_000, MemWorkingSetBytes: 500},
			{Namespace: "gone", Name: "orphan-7c9d8b6f4-abcde", MemWorkingSetBytes: 100},
		},
	}
	got := BuildConsumers(s, func(ns, name string) *corev1.Pod { return pods[ns+"/"+name] })

	cpu := got.CPU.Consumers
	if len(cpu) != 4 || cpu[0].Kind != "host" || cpu[0].Value != 1000 {
		t.Fatalf("k3s should rank as its own row: %+v", cpu)
	}
	if cpu[1].Kind != "service" || cpu[1].Name != "web" || cpu[1].Pods != 2 || cpu[1].Value != 500 {
		t.Fatalf("cpu ranking = %+v", cpu)
	}
	if cpu[2].Kind != "addon" || cpu[2].Name != "shop-db" || cpu[3].Kind != "system" || cpu[3].Name != "coredns" {
		t.Fatalf("cpu owners = %+v", cpu)
	}
	if got.CPU.Used != 2000 || got.CPU.Unattributed != 350 {
		t.Errorf("cpu used/unattributed = %d/%d", got.CPU.Used, got.CPU.Unattributed)
	}

	mem := got.Memory.Consumers
	if mem[0].Name != "shop-db" || mem[len(mem)-1].Name != "orphan" || mem[len(mem)-1].Namespace != "gone" {
		t.Errorf("memory ranking = %+v", mem)
	}
	if got.Memory.Unattributed != 10_000-7_600-2_000 {
		t.Errorf("memory unattributed = %d", got.Memory.Unattributed)
	}

	d := got.Disk
	if d.Images != 600_000 || d.Unattributed != 900_000-600_000-21_000 || d.Consumers[0].Name != "web" {
		t.Errorf("disk = %+v", d)
	}
}

func TestBuildConsumers_ImagesOnTheirOwnDiskAreNotCounted(t *testing.T) {
	s := kube.NodeUsageSummary{FSCapacityBytes: 100, FSUsedBytes: 40, ImageFSUsedBytes: 500, ImageFSShared: false}
	got := BuildConsumers(s, func(string, string) *corev1.Pod { return nil })
	if got.Disk.Images != 0 || got.Disk.Unattributed != 40 {
		t.Errorf("separate image fs leaked into the root disk: %+v", got.Disk)
	}
}
