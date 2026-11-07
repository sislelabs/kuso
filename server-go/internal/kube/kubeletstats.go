package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
)

// NodeFS is a node's real root-filesystem footprint as its kubelet
// reports it.
type NodeFS struct {
	CapacityBytes  int64
	AvailableBytes int64
}

// ErrKubeletForbidden means the kubelet refused the service account
// (RBAC lacks nodes/stats get).
var ErrKubeletForbidden = errors.New("kubelet stats forbidden")

// kubeletStatsForbiddenBackoff is how long a 403 parks the kubelet
// calls. The nodes/stats grant is ssh-applied by hand (the updater
// cannot grant RBAC), so it can land while the server is running; a
// permanent latch would have needed a restart to notice.
const kubeletStatsForbiddenBackoff = 10 * time.Minute

type kubeletStats struct {
	once      sync.Once
	client    *http.Client
	clientErr error

	mu             sync.Mutex
	forbiddenUntil time.Time
}

// kubeletStatsState hangs off the package rather than Client so a
// Client literal (as tests build them) needs no constructor.
var kubeletStatsState sync.Map // *rest.Config -> *kubeletStats

func (c *Client) kubeletStats() *kubeletStats {
	v, _ := kubeletStatsState.LoadOrStore(c.Config, &kubeletStats{})
	return v.(*kubeletStats)
}

// NodeFilesystems reads each node's real disk usage from its kubelet's
// /stats/summary, called directly on the node's InternalIP rather than
// through the apiserver's nodes/proxy subresource. The kubelet
// authorizes /stats/* as nodes/stats, which unlike nodes/proxy grants
// no exec/run into pods, so kuso-server can hold it.
//
// Why not the node object: ephemeral-storage Capacity minus Allocatable
// is the kubelet's fixed reservation, the same ~16GB whether the disk
// is empty or full, so it renders a constant "15GiB / 300GiB (5%)".
//
// Nodes are fetched in parallel with a per-node timeout. A node that
// fails is left out of the map (callers keep their fallback) and its
// error is returned in errs. After a 403 every call returns
// ErrKubeletForbidden without dialling, for kubeletStatsForbiddenBackoff.
func (c *Client) NodeFilesystems(ctx context.Context, nodes []*corev1.Node) (map[string]NodeFS, []error) {
	out := map[string]NodeFS{}
	if c == nil || c.Config == nil || len(nodes) == 0 {
		return out, nil
	}
	st := c.kubeletStats()
	st.mu.Lock()
	parked := time.Now().Before(st.forbiddenUntil)
	st.mu.Unlock()
	if parked {
		return out, []error{ErrKubeletForbidden}
	}
	st.once.Do(func() { st.client, st.clientErr = rest.HTTPClientFor(c.Config) })
	if st.clientErr != nil {
		return out, []error{fmt.Errorf("kubelet http client: %w", st.clientErr)}
	}

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	for _, n := range nodes {
		wg.Add(1)
		go func(n *corev1.Node) {
			defer wg.Done()
			fs, err := fetchNodeFS(ctx, st.client, n)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("node %s: %w", n.Name, err))
				return
			}
			out[n.Name] = fs
		}(n)
	}
	wg.Wait()
	for _, err := range errs {
		if errors.Is(err, ErrKubeletForbidden) {
			st.mu.Lock()
			st.forbiddenUntil = time.Now().Add(kubeletStatsForbiddenBackoff)
			st.mu.Unlock()
			break
		}
	}
	return out, errs
}

func fetchNodeFS(ctx context.Context, client *http.Client, n *corev1.Node) (NodeFS, error) {
	addr := ""
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			addr = a.Address
			break
		}
	}
	if addr == "" {
		return NodeFS{}, errors.New("node has no InternalIP")
	}
	port := int(n.Status.DaemonEndpoints.KubeletEndpoint.Port)
	if port == 0 {
		port = 10250
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	url := "https://" + net.JoinHostPort(addr, strconv.Itoa(port)) + "/stats/summary"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return NodeFS{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return NodeFS{}, err
	}
	defer resp.Body.Close()
	// The node-level summary is a few KB; the cap only guards a
	// misbehaving endpoint. Per-pod stats make it larger on busy nodes.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return NodeFS{}, err
	}
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return NodeFS{}, fmt.Errorf("%w: %d %.200s", ErrKubeletForbidden, resp.StatusCode, body)
	case resp.StatusCode != http.StatusOK:
		return NodeFS{}, fmt.Errorf("kubelet %d: %.200s", resp.StatusCode, body)
	}
	return ParseSummaryFS(body)
}

// ParseSummaryFS pulls node.fs out of a kubelet Summary API response.
// A missing fs block is an error, not a zero-valued success: zeros
// would render as an empty disk and could fire a bogus 100%-full alert.
func ParseSummaryFS(body []byte) (NodeFS, error) {
	var resp struct {
		Node struct {
			FS *struct {
				CapacityBytes  *int64 `json:"capacityBytes"`
				AvailableBytes *int64 `json:"availableBytes"`
				UsedBytes      *int64 `json:"usedBytes"`
			} `json:"fs"`
		} `json:"node"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return NodeFS{}, fmt.Errorf("decode summary: %w", err)
	}
	fs := resp.Node.FS
	if fs == nil || fs.CapacityBytes == nil || *fs.CapacityBytes <= 0 {
		return NodeFS{}, errors.New("summary has no node fs capacity")
	}
	out := NodeFS{CapacityBytes: *fs.CapacityBytes}
	switch {
	case fs.AvailableBytes != nil:
		out.AvailableBytes = *fs.AvailableBytes
	case fs.UsedBytes != nil:
		out.AvailableBytes = out.CapacityBytes - *fs.UsedBytes
	default:
		return NodeFS{}, errors.New("summary has neither available nor used bytes")
	}
	return out, nil
}
