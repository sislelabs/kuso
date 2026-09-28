// Pod sizing flags for `kuso project service add|set`:
//
//   kuso project service add <p> <svc> --size large       # preset (or "none")
//   kuso project service set <p> <svc> --size small
//   kuso project service set <p> <svc> --memory-limit 1536Mi --cpu-request 200m
//
// Omitting --size on add leaves sizing to the server's default pod size.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// podSizeNone is the --size value that means "no requests/limits".
const podSizeNone = "none"

var (
	serviceAddSize       string
	serviceSetSize       string
	serviceSetMemLimit   string
	serviceSetMemRequest string
	serviceSetCPURequest string
)

// podSizeResources renders a preset as the spec.resources map the server
// stores (mirrors server-go/internal/podsizes.Resources).
func podSizeResources(p kusoApi.PodSize) map[string]any {
	out := map[string]any{}
	setResourceQty(out, "requests", "cpu", p.CPURequest)
	setResourceQty(out, "requests", "memory", p.MemoryRequest)
	setResourceQty(out, "limits", "cpu", p.CPULimit)
	setResourceQty(out, "limits", "memory", p.MemoryLimit)
	return out
}

// setResourceQty sets res[section][key] = qty; an empty qty removes the
// key (and the section once it's empty).
func setResourceQty(res map[string]any, section, key, qty string) {
	qty = strings.TrimSpace(qty)
	m, _ := res[section].(map[string]any)
	if qty == "" {
		if m != nil {
			delete(m, key)
			if len(m) == 0 {
				delete(res, section)
			}
		}
		return
	}
	if m == nil {
		m = map[string]any{}
		res[section] = m
	}
	m[key] = qty
}

// presetResources looks up a preset by name on the server. "none" yields
// an empty map (no requests/limits).
func presetResources(name string) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == podSizeNone {
		return map[string]any{}, nil
	}
	resp, err := api.ListPodSizes()
	if err := checkRespErr(resp, err); err != nil {
		return nil, fmt.Errorf("list pod sizes: %w", err)
	}
	var sizes []kusoApi.PodSize
	if err := json.Unmarshal(resp.Body(), &sizes); err != nil {
		return nil, fmt.Errorf("decode pod sizes: %w", err)
	}
	names := make([]string, 0, len(sizes))
	for _, p := range sizes {
		if p.Name == name {
			return podSizeResources(p), nil
		}
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("unknown pod size %q (available: %s, or %s)", name, strings.Join(names, ", "), podSizeNone)
}

// serviceAddResources is the create request's resources: nil (server
// default) unless --size was passed.
func serviceAddResources(cmd *cobra.Command) (*map[string]any, error) {
	if !cmd.Flags().Changed("size") {
		return nil, nil
	}
	res, err := presetResources(serviceAddSize)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// serviceSetResources builds the PATCH resources block, or nil when no
// sizing flag was passed. The server replaces resources verbatim, so the
// per-quantity flags merge onto the live block (or onto --size's preset).
func serviceSetResources(cmd *cobra.Command, project, service string) (*map[string]any, error) {
	f := cmd.Flags()
	perField := f.Changed("memory-limit") || f.Changed("memory-request") || f.Changed("cpu-request")
	if !f.Changed("size") && !perField {
		return nil, nil
	}
	var res map[string]any
	if f.Changed("size") {
		p, err := presetResources(serviceSetSize)
		if err != nil {
			return nil, err
		}
		res = p
	} else {
		cur, err := api.GetService(project, service)
		if err := checkRespErr(cur, err); err != nil {
			return nil, fmt.Errorf("fetch current service spec: %w", err)
		}
		var wire struct {
			Spec struct {
				Resources map[string]any `json:"resources"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(cur.Body(), &wire); err != nil {
			return nil, fmt.Errorf("decode current service: %w", err)
		}
		res = wire.Spec.Resources
		if res == nil {
			res = map[string]any{}
		}
	}
	if f.Changed("memory-limit") {
		setResourceQty(res, "limits", "memory", serviceSetMemLimit)
	}
	if f.Changed("memory-request") {
		setResourceQty(res, "requests", "memory", serviceSetMemRequest)
	}
	if f.Changed("cpu-request") {
		setResourceQty(res, "requests", "cpu", serviceSetCPURequest)
	}
	return &res, nil
}

func init() {
	for _, c := range []*cobra.Command{serviceAddCmd, serviceAddTopCmd} {
		c.Flags().StringVar(&serviceAddSize, "size", "", "pod-size preset (see `kuso instance-config podsize list`), or none; default: the instance's default pod size")
	}
	for _, c := range []*cobra.Command{serviceSetCmd, serviceSetTopCmd} {
		c.Flags().StringVar(&serviceSetSize, "size", "", "apply a pod-size preset's requests/limits (replaces current), or none to clear")
		c.Flags().StringVar(&serviceSetMemLimit, "memory-limit", "", "memory limit, e.g. 1Gi (empty removes it)")
		c.Flags().StringVar(&serviceSetMemRequest, "memory-request", "", "memory request, e.g. 256Mi (empty removes it)")
		c.Flags().StringVar(&serviceSetCPURequest, "cpu-request", "", "CPU request, e.g. 100m (empty removes it)")
	}
}
