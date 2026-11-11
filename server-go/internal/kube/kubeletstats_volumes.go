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
func (c *Client) NodePVCUsage(ctx context.Context, n *corev1.Node) (map[string]int64, error) {
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

// ParseSummaryPVCUsage extracts per-PVC used bytes from a kubelet
// summary. A PVC mounted by several pods on the node reports the same
// volume, so the largest reading wins.
func ParseSummaryPVCUsage(body []byte) (map[string]int64, error) {
	var sum struct {
		Pods []struct {
			Volume []struct {
				UsedBytes *int64 `json:"usedBytes"`
				PVCRef    *struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"pvcRef"`
			} `json:"volume"`
		} `json:"pods"`
	}
	if err := json.Unmarshal(body, &sum); err != nil {
		return nil, fmt.Errorf("decode kubelet summary: %w", err)
	}
	out := map[string]int64{}
	for _, p := range sum.Pods {
		for _, v := range p.Volume {
			if v.PVCRef == nil || v.UsedBytes == nil {
				continue
			}
			k := v.PVCRef.Namespace + "/" + v.PVCRef.Name
			if *v.UsedBytes > out[k] {
				out[k] = *v.UsedBytes
			}
		}
	}
	return out, nil
}
