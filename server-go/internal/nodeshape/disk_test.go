package nodeshape

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func nodeWithStorage(name string, capBytes, allocBytes int64) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceEphemeralStorage: *resource.NewQuantity(capBytes, resource.BinarySI),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceEphemeralStorage: *resource.NewQuantity(allocBytes, resource.BinarySI),
			},
		},
	}
}

// capacity/allocatable ephemeral-storage differ by a FIXED kubelet
// reservation, so deriving "used" from them reported 5% on a node that
// was 83% full. When the caller has real kubelet fs numbers, they win.
func TestBuildSummaries_PrefersRealDiskOverStaticReservation(t *testing.T) {
	const cap322, alloc306 = 322_200_000_000, 306_100_000_000
	nodes := []corev1.Node{nodeWithStorage("kuso-worker", cap322, alloc306)}

	got := BuildSummaries(nodes, nil, map[string]Usage{
		"kuso-worker": {DiskCapacityBytes: cap322, DiskAvailableBytes: 53_000_000_000},
	})

	if len(got) != 1 {
		t.Fatalf("summaries = %d", len(got))
	}
	s := got[0]
	if s.DiskAvailableBytes != 53_000_000_000 {
		t.Errorf("DiskAvailableBytes = %d, want the real 53GB", s.DiskAvailableBytes)
	}
	usedPct := 100 * float64(s.DiskCapacityBytes-s.DiskAvailableBytes) / float64(s.DiskCapacityBytes)
	if usedPct < 75 || usedPct > 90 {
		t.Errorf("used%% = %.1f, want ~83 (static reservation reported ~5)", usedPct)
	}
}

// A node whose kubelet didn't answer still renders, using the static
// figures rather than claiming an empty disk.
func TestBuildSummaries_FallsBackToStaticWhenNoRealDisk(t *testing.T) {
	const cap322, alloc306 = 322_200_000_000, 306_100_000_000
	got := BuildSummaries([]corev1.Node{nodeWithStorage("n", cap322, alloc306)}, nil, nil)

	if got[0].DiskCapacityBytes != cap322 {
		t.Errorf("capacity = %d, want the static %d", got[0].DiskCapacityBytes, int64(cap322))
	}
	if got[0].DiskAvailableBytes != alloc306 {
		t.Errorf("available = %d, want the static fallback %d", got[0].DiskAvailableBytes, int64(alloc306))
	}
}
