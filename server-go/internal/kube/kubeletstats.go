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
	var mu sync.Mutex
	errs := c.eachSummary(ctx, nodes, func(n *corev1.Node, body []byte) error {
		fs, err := ParseSummaryFS(body)
		if err != nil {
			return err
		}
		mu.Lock()
		out[n.Name] = fs
		mu.Unlock()
		return nil
	})
	return out, errs
}

// NodesPodUsage reads every node's kubelet summary and returns the
// per-pod usage of all of them, keyed by node name. Same client,
// per-node timeout and 403 backoff as NodeFilesystems; a node that
// fails is absent from the map and its error is in errs.
func (c *Client) NodesPodUsage(ctx context.Context, nodes []*corev1.Node) (map[string][]PodUsage, []error) {
	out := map[string][]PodUsage{}
	if c == nil || c.Config == nil || len(nodes) == 0 {
		return out, nil
	}
	var mu sync.Mutex
	errs := c.eachSummary(ctx, nodes, func(n *corev1.Node, body []byte) error {
		sum, err := ParseSummaryUsage(body)
		if err != nil {
			return err
		}
		mu.Lock()
		out[n.Name] = sum.Pods
		mu.Unlock()
		return nil
	})
	return out, errs
}

// eachSummary fetches every node's /stats/summary in parallel and hands
// each body to fn. Errors come back wrapped with the node name; a 403
// parks the kubelet calls for kubeletStatsForbiddenBackoff.
func (c *Client) eachSummary(ctx context.Context, nodes []*corev1.Node, fn func(*corev1.Node, []byte) error) []error {
	st, err := c.kubeletStatsClient()
	if err != nil {
		return []error{err}
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
			body, err := fetchSummary(ctx, st.client, n)
			if err == nil {
				err = fn(n, body)
			}
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("node %s: %w", n.Name, err))
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	for _, err := range errs {
		if errors.Is(err, ErrKubeletForbidden) {
			st.park()
			break
		}
	}
	return errs
}

// kubeletStatsClient returns the shared kubelet client, or
// ErrKubeletForbidden while a 403 has the calls parked.
func (c *Client) kubeletStatsClient() (*kubeletStats, error) {
	st := c.kubeletStats()
	st.mu.Lock()
	parked := time.Now().Before(st.forbiddenUntil)
	st.mu.Unlock()
	if parked {
		return nil, ErrKubeletForbidden
	}
	st.once.Do(func() { st.client, st.clientErr = rest.HTTPClientFor(c.Config) })
	if st.clientErr != nil {
		return nil, fmt.Errorf("kubelet http client: %w", st.clientErr)
	}
	return st, nil
}

func (st *kubeletStats) park() {
	st.mu.Lock()
	st.forbiddenUntil = time.Now().Add(kubeletStatsForbiddenBackoff)
	st.mu.Unlock()
}

// NodeUsage reads one node's kubelet /stats/summary with its per-pod
// breakdown. Shares NodeFilesystems' client and 403 backoff.
func (c *Client) NodeUsage(ctx context.Context, n *corev1.Node) (NodeUsageSummary, error) {
	if c == nil || c.Config == nil || n == nil {
		return NodeUsageSummary{}, errors.New("kube client not configured")
	}
	st, err := c.kubeletStatsClient()
	if err != nil {
		return NodeUsageSummary{}, err
	}
	body, err := fetchSummary(ctx, st.client, n)
	if errors.Is(err, ErrKubeletForbidden) {
		st.park()
	}
	if err != nil {
		return NodeUsageSummary{}, err
	}
	return ParseSummaryUsage(body)
}

func fetchSummary(ctx context.Context, client *http.Client, n *corev1.Node) ([]byte, error) {
	addr := ""
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			addr = a.Address
			break
		}
	}
	if addr == "" {
		return nil, errors.New("node has no InternalIP")
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
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// The node-level summary is a few KB; the cap only guards a
	// misbehaving endpoint. Per-pod stats make it larger on busy nodes.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: %d %.200s", ErrKubeletForbidden, resp.StatusCode, body)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("kubelet %d: %.200s", resp.StatusCode, body)
	}
	return body, nil
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

// PodUsage is one pod's live usage from the kubelet summary.
// EphemeralBytes is the pod's share of the node disk: container
// writable layers, logs and disk-backed emptyDirs.
type PodUsage struct {
	Namespace          string
	Name               string
	CPUNanoCores       uint64
	MemWorkingSetBytes uint64
	EphemeralBytes     uint64
}

// NodeUsageSummary is the node totals plus per-pod usage. Memory is
// working set, the figure metrics-server reports, so the per-pod rows
// add up against the node tile.
type NodeUsageSummary struct {
	CPUNanoCores       uint64
	MemWorkingSetBytes uint64
	FSCapacityBytes    uint64
	FSUsedBytes        uint64
	// ImageFS is the container image store; ImageFSShared reports
	// whether it sits on the node root filesystem (it does on k3s).
	ImageFSUsedBytes uint64
	ImageFSShared    bool
	Pods             []PodUsage
	// Host is the node-level processes the kubelet tracks outside pods
	// ("kubelet", "runtime"); on k3s "kubelet" is the whole k3s
	// process, containerd included.
	Host []PodUsage
}

// ParseSummaryUsage pulls node totals and per-pod usage out of a
// kubelet Summary API response.
func ParseSummaryUsage(body []byte) (NodeUsageSummary, error) {
	type fsStats struct {
		CapacityBytes uint64 `json:"capacityBytes"`
		UsedBytes     uint64 `json:"usedBytes"`
	}
	type cpuStats struct {
		UsageNanoCores uint64 `json:"usageNanoCores"`
	}
	type memStats struct {
		WorkingSetBytes uint64 `json:"workingSetBytes"`
	}
	var resp struct {
		Node struct {
			CPU     *cpuStats `json:"cpu"`
			Memory  *memStats `json:"memory"`
			FS      *fsStats  `json:"fs"`
			Runtime *struct {
				ImageFS *fsStats `json:"imageFs"`
			} `json:"runtime"`
			SystemContainers []struct {
				Name   string    `json:"name"`
				CPU    *cpuStats `json:"cpu"`
				Memory *memStats `json:"memory"`
			} `json:"systemContainers"`
		} `json:"node"`
		Pods []struct {
			PodRef struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"podRef"`
			CPU       *cpuStats `json:"cpu"`
			Memory    *memStats `json:"memory"`
			Ephemeral *fsStats  `json:"ephemeral-storage"`
		} `json:"pods"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return NodeUsageSummary{}, fmt.Errorf("decode summary: %w", err)
	}
	n := resp.Node
	if n.FS == nil || n.FS.CapacityBytes == 0 {
		return NodeUsageSummary{}, errors.New("summary has no node fs capacity")
	}
	out := NodeUsageSummary{FSCapacityBytes: n.FS.CapacityBytes, FSUsedBytes: n.FS.UsedBytes}
	if n.CPU != nil {
		out.CPUNanoCores = n.CPU.UsageNanoCores
	}
	if n.Memory != nil {
		out.MemWorkingSetBytes = n.Memory.WorkingSetBytes
	}
	if n.Runtime != nil && n.Runtime.ImageFS != nil {
		out.ImageFSUsedBytes = n.Runtime.ImageFS.UsedBytes
		out.ImageFSShared = n.Runtime.ImageFS.CapacityBytes == n.FS.CapacityBytes
	}
	for _, c := range n.SystemContainers {
		// "pods" is the pod cgroup total, already broken out per pod.
		if c.Name != "kubelet" && c.Name != "runtime" {
			continue
		}
		u := PodUsage{Name: c.Name}
		if c.CPU != nil {
			u.CPUNanoCores = c.CPU.UsageNanoCores
		}
		if c.Memory != nil {
			u.MemWorkingSetBytes = c.Memory.WorkingSetBytes
		}
		out.Host = append(out.Host, u)
	}
	for _, p := range resp.Pods {
		u := PodUsage{Namespace: p.PodRef.Namespace, Name: p.PodRef.Name}
		if p.CPU != nil {
			u.CPUNanoCores = p.CPU.UsageNanoCores
		}
		if p.Memory != nil {
			u.MemWorkingSetBytes = p.Memory.WorkingSetBytes
		}
		if p.Ephemeral != nil {
			u.EphemeralBytes = p.Ephemeral.UsedBytes
		}
		out.Pods = append(out.Pods, u)
	}
	return out, nil
}
