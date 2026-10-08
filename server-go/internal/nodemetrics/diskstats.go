package nodemetrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// nodeFS is the real filesystem footprint of a node's root disk, as
// the kubelet sees it.
type nodeFS struct {
	capacityBytes  int64
	availableBytes int64
	usedBytes      int64
}

// parseNodeFS pulls the node-level fs block out of a kubelet Summary
// API response.
//
// We read this instead of the node object's ephemeral-storage
// capacity/allocatable pair because that pair is STATIC: allocatable
// is capacity minus the kubelet's reservation, so the difference is a
// fixed ~16GB on every node no matter how full the disk is. Rendering
// it as usage showed a constant "15.0GiB / 300GiB (5%)" on three nodes
// that were really at 83%, 81% and 62%, and left the disk-pressure
// alert permanently unfireable because it derives its percentage from
// the same two numbers.
//
// A missing or unparseable fs block is an error rather than a
// zero-valued success: zeros would render as an empty disk and could
// fire a bogus 100%-full alert.
func parseNodeFS(body []byte) (nodeFS, error) {
	var resp struct {
		Node struct {
			NodeName string `json:"nodeName"`
			FS       *struct {
				CapacityBytes  *int64 `json:"capacityBytes"`
				AvailableBytes *int64 `json:"availableBytes"`
				UsedBytes      *int64 `json:"usedBytes"`
			} `json:"fs"`
		} `json:"node"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nodeFS{}, fmt.Errorf("decode summary: %w", err)
	}
	fs := resp.Node.FS
	if fs == nil || fs.CapacityBytes == nil || *fs.CapacityBytes <= 0 {
		return nodeFS{}, errors.New("summary has no node fs capacity")
	}
	out := nodeFS{capacityBytes: *fs.CapacityBytes}
	if fs.AvailableBytes != nil {
		out.availableBytes = *fs.AvailableBytes
	}
	if fs.UsedBytes != nil {
		out.usedBytes = *fs.UsedBytes
	}
	// Some kubelets report only one of used/available. Derive the
	// other so callers never have to care which arrived.
	if out.availableBytes == 0 && out.usedBytes > 0 {
		out.availableBytes = out.capacityBytes - out.usedBytes
	}
	if out.usedBytes == 0 && out.availableBytes > 0 {
		out.usedBytes = out.capacityBytes - out.availableBytes
	}
	return out, nil
}

// diskStats fetches real per-node disk usage from each kubelet's
// Summary API, proxied through the apiserver.
//
// Best-effort, exactly like metricsServerUsage: one node that fails to
// answer leaves that node out of the map, and the caller falls back to
// the static figures rather than writing a row that claims the disk is
// empty. Per-node timeout so one wedged kubelet can't stall the tick.
//
// Failures are summarised in one WARN per tick. The proxy path needs
// nodes/proxy, which kuso-server is deliberately not granted (kubelet
// exec escalation), and a silent `continue` hid a week of blank disk
// metrics.
func (s *Sampler) diskStats(ctx context.Context, nodeNames []string) map[string]nodeFS {
	out := map[string]nodeFS{}
	if s.diskForbidden.Load() {
		return out
	}
	rest := s.Kube.Clientset.Discovery().RESTClient()
	if rest == nil {
		return out
	}
	var firstErr error
	failed, forbidden := 0, false
	for _, name := range nodeNames {
		nctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		body, err := rest.Get().
			AbsPath(fmt.Sprintf("/api/v1/nodes/%s/proxy/stats/summary", name)).
			DoRaw(nctx)
		cancel()
		var fs nodeFS
		if err != nil && len(body) > 0 {
			// The discovery REST client can't decode a Status body, so
			// err alone reads "unknown"; the body names the forbidden
			// resource.
			err = fmt.Errorf("%w: %.200s", err, body)
			forbidden = forbidden || bytes.Contains(body, []byte(`"reason":"Forbidden"`))
		}
		if err == nil {
			fs, err = parseNodeFS(body)
		}
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("node %s: %w", name, err)
			}
			continue
		}
		out[name] = fs
	}
	if failed > 0 {
		logger := s.Logger
		if logger == nil {
			logger = slog.Default()
		}
		if forbidden {
			s.diskForbidden.Store(true)
			logger.Warn("nodemetrics: kubelet disk summary forbidden (nodes/proxy is not granted); disk figures disabled until restart",
				"failedNodes", failed, "nodes", len(nodeNames), "firstErr", firstErr)
			return out
		}
		logger.Warn("nodemetrics: kubelet disk summary unavailable; disk figures left blank",
			"failedNodes", failed, "nodes", len(nodeNames), "firstErr", firstErr)
	}
	return out
}
