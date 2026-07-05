// `kuso project addon placement` — pin an addon's StatefulSet to
// specific nodes, mirroring service placement. Editor-gated.
//
//   kuso project addon placement show <project> <addon>
//   kuso project addon placement set  <project> <addon> --label disk=ssd --label region=eu
//   kuso project addon placement set  <project> <addon> --node worker-3
//   kuso project addon placement clear <project> <addon>
//
// Labels are ANDed (a node must carry every label); nodes is an explicit
// allow-list. `set` replaces the whole placement; `clear` removes it so
// the addon can schedule anywhere.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"strings"

	"kuso/pkg/kusoApi"

	"github.com/spf13/cobra"
)

var (
	addonPlacementLabels []string
	addonPlacementNodes  []string
)

var addonPlacementCmd = &cobra.Command{
	Use:   "placement",
	Short: "Pin an addon to specific nodes (editor)",
}

var addonPlacementShowCmd = &cobra.Command{
	Use:   "show <project> <addon>",
	Short: "Show an addon's current node placement",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// Placement lives on the addon CR spec; read it from the addons list.
		resp, err := api.GetAddonsForProject(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("get addons: %w", err)
		}
		var addons []map[string]any
		if err := json.Unmarshal(resp.Body(), &addons); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		for _, a := range addons {
			if resourceName(a) != args[1] {
				continue
			}
			placement := mapAt(mapAt(a, "spec"), "placement")
			if len(placement) == 0 {
				fmt.Printf("addon %s/%s has no placement (schedules anywhere)\n", args[0], args[1])
				return nil
			}
			return jsonOut(placement)
		}
		return fmt.Errorf("addon %q not found in project %q", args[1], args[0])
	},
}

var addonPlacementSetCmd = &cobra.Command{
	Use:   "set <project> <addon>",
	Short: "Set an addon's node placement (replaces existing)",
	Long: `Pin the addon's pod to nodes matching ALL --label selectors and/or
listed in --node. Replaces any existing placement wholesale — pass every
label/node the addon should have. Use 'placement clear' to remove.`,
	Args: cobra.ExactArgs(2),
	Example: `  kuso project addon placement set myproj myproj-db --label disk=ssd
  kuso project addon placement set myproj myproj-db --node worker-3 --node worker-4`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if len(addonPlacementLabels) == 0 && len(addonPlacementNodes) == 0 {
			return fmt.Errorf("provide at least one --label or --node (use 'placement clear' to remove placement)")
		}
		labels := map[string]string{}
		for _, l := range addonPlacementLabels {
			k, v, ok := strings.Cut(l, "=")
			if !ok || k == "" {
				return fmt.Errorf("invalid --label %q: want key=value", l)
			}
			labels[k] = v
		}
		resp, err := api.SetAddonPlacement(args[0], args[1], kusoApi.AddonPlacement{
			Labels: labels,
			Nodes:  addonPlacementNodes,
		})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("set addon placement: %w", err)
		}
		fmt.Printf("addon %s/%s placement set (%d label(s), %d node(s))\n",
			args[0], args[1], len(labels), len(addonPlacementNodes))
		return nil
	},
}

var addonPlacementClearCmd = &cobra.Command{
	Use:   "clear <project> <addon>",
	Short: "Remove an addon's node placement (schedules anywhere)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.SetAddonPlacement(args[0], args[1], kusoApi.AddonPlacement{})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("clear addon placement: %w", err)
		}
		fmt.Printf("addon %s/%s placement cleared\n", args[0], args[1])
		return nil
	},
}

func init() {
	projectAddonCmd.AddCommand(addonPlacementCmd)
	addonPlacementCmd.AddCommand(addonPlacementShowCmd)
	addonPlacementCmd.AddCommand(addonPlacementSetCmd)
	addonPlacementCmd.AddCommand(addonPlacementClearCmd)

	addonPlacementSetCmd.Flags().StringArrayVar(&addonPlacementLabels, "label", nil, "node label selector, repeatable: key=value")
	addonPlacementSetCmd.Flags().StringArrayVar(&addonPlacementNodes, "node", nil, "explicit node name, repeatable")
}
