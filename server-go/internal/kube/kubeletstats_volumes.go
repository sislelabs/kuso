package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// NodePVCUsage reads one node's kubelet /stats/summary and returns the
// bytes used on every PVC-backed pod volume, keyed "<namespace>/<pvc>".
// Shares NodeFilesystems' client and 403 backoff (nodes/stats get).
func (c *Client) NodePVCUsage(ctx context.Context, n *corev1.Node) (map[string]PVCUsage, error) {
	if c == nil || c.Config == nil || n == nil {
		return nil, errors.New("kube client not configured")
	}
	st, err := c.kubeletStatsClient()
	if err != nil {
		return nil, err
	}
	body, err := fetchSummary(ctx, st.client, n)
	if errors.Is(err, ErrKubeletForbidden) {
		st.park()
	}
	if err != nil {
		return nil, err
	}
	return ParseSummaryPVCUsage(body)
}

// PVCUsage is one PVC's kubelet volume stats. CapacityBytes is the size
// of the filesystem the volume lives on: about the PVC size for a
// size-enforced volume, the whole node disk for local-path, where
// UsedBytes is then the node disk's usage, not the volume directory's.
type PVCUsage struct {
	UsedBytes     int64
	CapacityBytes int64
}

// ParseSummaryPVCUsage extracts per-PVC usage from a kubelet summary. A
// PVC mounted by several pods on the node reports the same volume, so
// the largest reading wins.
func ParseSummaryPVCUsage(body []byte) (map[string]PVCUsage, error) {
	var sum struct {
		Pods []struct {
			Volume []struct {
				UsedBytes     *int64 `json:"usedBytes"`
				CapacityBytes *int64 `json:"capacityBytes"`
				PVCRef        *struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"pvcRef"`
			} `json:"volume"`
		} `json:"pods"`
	}
	if err := json.Unmarshal(body, &sum); err != nil {
		return nil, fmt.Errorf("decode kubelet summary: %w", err)
	}
	out := map[string]PVCUsage{}
	for _, p := range sum.Pods {
		for _, v := range p.Volume {
			if v.PVCRef == nil || v.UsedBytes == nil {
				continue
			}
			k := v.PVCRef.Namespace + "/" + v.PVCRef.Name
			if *v.UsedBytes > out[k].UsedBytes {
				u := PVCUsage{UsedBytes: *v.UsedBytes}
				if v.CapacityBytes != nil {
					u.CapacityBytes = *v.CapacityBytes
				}
				out[k] = u
			}
		}
	}
	return out, nil
}
