// `kuso env-group` — manage env groups (clone every service + addon in a
// project into a new named environment set, e.g. staging / client-demo).
//
// Distinct from:
//   - `kuso environment` — a single service's extra env (one KusoEnvironment)
//   - `kuso env`         — env VARIABLES on a service
//
// The server CRUD (/api/projects/{p}/env-groups) shipped without any CLI;
// this closes that gap so env groups are scriptable / agent-drivable.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"kuso/pkg/kusoApi"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var (
	envGroupAddonShare []string
	envGroupDeleteYes  bool
)

var envGroupCmd = &cobra.Command{
	Use:     "env-group",
	Aliases: []string{"env-groups", "envgroup"},
	Short:   "Manage env groups (staging/qa/demo across every service in a project)",
	Long: `Manage env groups: clone every service + addon in a project into a new
named environment set (e.g. "staging", "client-demo").

By default each addon gets its OWN fresh empty datastore so the group never
touches production data. Use --share-addon <name> to reuse production's
datastore for a specific addon instead.

Distinct from "kuso environment" (a single service's extra env) and
"kuso env" (env VARIABLES on a service).`,
}

var envGroupListCmd = &cobra.Command{
	Use:     "list <project>",
	Aliases: []string{"ls"},
	Short:   "List env groups in a project",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListEnvGroups(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list env-groups: %w", err)
		}
		var items []map[string]any
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		sort.Slice(items, func(i, j int) bool {
			return asString(items[i]["name"]) < asString(items[j]["name"])
		})
		switch outputFormat {
		case "json":
			return jsonOut(items)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "KIND", "SERVICES", "ADDONS"})
			for _, e := range items {
				t.Append([]string{
					asString(e["name"]),
					asString(e["kind"]),
					joinAny(e["services"]),
					joinAny(e["addons"]),
				})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

var envGroupGetCmd = &cobra.Command{
	Use:   "get <project> <name>",
	Short: "Show one env group's services + addons",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetEnvGroup(args[0], args[1])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("get env-group: %w", err)
		}
		var e map[string]any
		if err := json.Unmarshal(resp.Body(), &e); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if outputFormat == "json" {
			return jsonOut(e)
		}
		fmt.Printf("env-group %s/%s (kind=%s)\n", args[0], asString(e["name"]), asString(e["kind"]))
		fmt.Printf("  services: %s\n", joinAny(e["services"]))
		fmt.Printf("  addons:   %s\n", joinAny(e["addons"]))
		return nil
	},
}

var envGroupCreateCmd = &cobra.Command{
	Use:   "create <project> <name>",
	Short: "Create an env group (clones every service + addon into a new env set)",
	Args:  cobra.ExactArgs(2),
	Example: `  kuso env-group create tickero staging
  kuso env-group create tickero staging --share-addon redis   # reuse prod redis, fresh DB`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		policy := map[string]string{}
		for _, a := range envGroupAddonShare {
			a = strings.TrimSpace(a)
			if a != "" {
				policy[a] = "shared"
			}
		}
		req := kusoApi.CreateEnvGroupRequest{Name: args[1], AddonPolicy: policy}
		resp, err := api.CreateEnvGroup(args[0], req)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("create env-group: %w", err)
		}
		msg, err := envGroupCreateMsg(args[0], args[1], resp.Body())
		if err != nil {
			return err
		}
		fmt.Print(msg)
		return nil
	},
}

// envGroupCreateMsg renders the create response: the literals the server
// retargeted from a sibling's production URL to its clone, and the
// project-domain URLs it couldn't map (which may still reach production).
func envGroupCreateMsg(project, name string, body []byte) (string, error) {
	var resp struct {
		RewrittenEnvVars []string `json:"rewrittenEnvVars"`
		Warnings         []string `json:"warnings"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &resp); err != nil {
			return "", fmt.Errorf("decode response: %w", err)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "env-group %s/%s created (addons default to fresh; --share-addon to reuse prod)\n", project, name)
	if len(resp.RewrittenEnvVars) > 0 {
		b.WriteString("env vars retargeted at this group's clones:\n")
		for _, k := range resp.RewrittenEnvVars {
			fmt.Fprintf(&b, "  %s\n", k)
		}
	}
	if len(resp.Warnings) > 0 {
		b.WriteString("WARNING: these env vars may still point at production; review them:\n")
		for _, w := range resp.Warnings {
			fmt.Fprintf(&b, "  %s\n", w)
		}
	}
	return b.String(), nil
}

var envGroupDeleteCmd = &cobra.Command{
	Use:     "delete <project> <name>",
	Aliases: []string{"rm"},
	Short:   "Delete an env group (production cannot be deleted)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(envGroupDeleteYes,
			fmt.Sprintf("Delete env-group %s/%s (tears down its services' envs + fresh addons)?", args[0], args[1])); err != nil {
			return err
		}
		resp, err := api.DeleteEnvGroup(args[0], args[1])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("delete env-group: %w", err)
		}
		fmt.Printf("env-group %s/%s deleted\n", args[0], args[1])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(envGroupCmd)
	envGroupCmd.AddCommand(envGroupListCmd)
	envGroupCmd.AddCommand(envGroupGetCmd)
	envGroupCmd.AddCommand(envGroupCreateCmd)
	envGroupCmd.AddCommand(envGroupDeleteCmd)
	envGroupListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")
	envGroupGetCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")
	envGroupCreateCmd.Flags().StringSliceVar(&envGroupAddonShare, "share-addon", nil, "addon short-name(s) to share with production instead of cloning fresh (repeatable)")
	envGroupDeleteCmd.Flags().BoolVarP(&envGroupDeleteYes, "yes", "y", false, "skip the confirmation prompt")
}
