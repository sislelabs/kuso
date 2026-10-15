// service_placement.go: `kuso service placement show|set|clear` — pin a
// service's pods to nodes, mirroring `kuso project addon placement`.
// Labels are ANDed; nodes is an explicit allow-list. The server refuses a
// placement no node matches.

package kusoCli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var (
	servicePlacementLabels []string
	servicePlacementNodes  []string
	servicePlacementOutput string
)

// buildServicePlacement turns --label k=v / --node n into the PATCH block.
func buildServicePlacement(labels, nodes []string) (*kusoApi.PatchPlacementRequest, error) {
	if len(labels) == 0 && len(nodes) == 0 {
		return nil, fmt.Errorf("provide at least one --label or --node (use 'placement clear' to drop the override)")
	}
	p := &kusoApi.PatchPlacementRequest{}
	for _, l := range labels {
		k, v, ok := strings.Cut(l, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("invalid --label %q: want key=value", l)
		}
		if p.Labels == nil {
			p.Labels = map[string]string{}
		}
		p.Labels[strings.TrimSpace(k)] = v
	}
	for _, n := range nodes {
		if n = strings.TrimSpace(n); n != "" {
			p.Nodes = append(p.Nodes, n)
		}
	}
	return p, nil
}

func newServicePlacementCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "placement",
		Short: "Pin a service's pods to specific nodes",
	}

	show := &cobra.Command{
		Use:   "show <project> <service>",
		Short: "Show a service's node placement override",
		Example: `  kuso service placement show shop api
  kuso service placement show shop api -o json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			cur, err := fetchServiceSpec(args[0], args[1])
			if err != nil {
				return err
			}
			p := cur.Spec.Placement
			if servicePlacementOutput == "json" {
				if p == nil {
					p = &kusoApi.PatchPlacementRequest{}
				}
				return jsonOut(p)
			}
			if p == nil {
				fmt.Printf("service %s/%s has no placement override (uses the project default)\n", args[0], args[1])
				return nil
			}
			if len(p.Labels) == 0 && len(p.Nodes) == 0 {
				fmt.Printf("service %s/%s: empty placement (schedules on any node, ignoring the project default)\n", args[0], args[1])
				return nil
			}
			fmt.Printf("service %s/%s placement:\n", args[0], args[1])
			keys := make([]string, 0, len(p.Labels))
			for k := range p.Labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("  label %s=%s\n", k, p.Labels[k])
			}
			for _, n := range p.Nodes {
				fmt.Printf("  node  %s\n", n)
			}
			return nil
		},
	}
	// Own variable, not the shared outputFormat global, so this command's
	// default can never leak into another command's -o.
	show.Flags().StringVarP(&servicePlacementOutput, "output", "o", "table", "output format [table, json]")

	set := &cobra.Command{
		Use:   "set <project> <service>",
		Short: "Set a service's node placement (replaces the existing one; pods reschedule)",
		Long: `Pin the service's pods to nodes carrying ALL --label selectors and/or
listed in --node. Replaces any existing placement. The server refuses a
placement no node matches. Pods are rescheduled onto matching nodes.`,
		Example: `  kuso service placement set shop api --label region=eu
  kuso service placement set shop api --node worker-3 --node worker-4`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			p, err := buildServicePlacement(servicePlacementLabels, servicePlacementNodes)
			if err != nil {
				return err
			}
			resp, err := api.PatchService(args[0], args[1], kusoApi.PatchServiceRequest{Placement: p})
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("set placement: %w", err)
			}
			fmt.Printf("service %s/%s placement set (%d label(s), %d node(s))\n", args[0], args[1], len(p.Labels), len(p.Nodes))
			return nil
		},
	}
	set.Flags().StringArrayVar(&servicePlacementLabels, "label", nil, "node label selector key=value, repeatable (ANDed)")
	set.Flags().StringArrayVar(&servicePlacementNodes, "node", nil, "explicit node name, repeatable")

	clear := &cobra.Command{
		Use:     "clear <project> <service>",
		Short:   "Drop the service's placement override (falls back to the project default)",
		Example: `  kuso service placement clear shop api`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			resp, err := api.PatchService(args[0], args[1], kusoApi.PatchServiceRequest{
				Placement: &kusoApi.PatchPlacementRequest{Clear: true},
			})
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("clear placement: %w", err)
			}
			fmt.Printf("service %s/%s placement cleared (project default applies)\n", args[0], args[1])
			return nil
		},
	}

	group.AddCommand(show, set, clear)
	return group
}

func init() {
	projectServiceCmd.AddCommand(newServicePlacementCmd())
	serviceCmd.AddCommand(newServicePlacementCmd())
}
