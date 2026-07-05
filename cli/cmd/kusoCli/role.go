// `kuso role` — RBAC role management (admin). Mirrors the admin Roles
// page. Every subcommand requires instance-admin (user:write); you get a
// 403 otherwise.
//
//   kuso role list [--full] [-o json]
//   kuso role get <id> [-o json]
//   kuso role create --name editor [--description '...'] --permission project:read --permission project:write
//   kuso role edit <id> --name '...' [--permission ...]   (replaces the full permission set)
//   kuso role delete <id>
//
// `kuso get roles` is a convenience alias for `kuso role list` — the id
// column feeds `kuso user create --role-id` and `kuso group role`.

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
	roleFull        bool
	roleName        string
	roleDescription string
	rolePermissions []string
)

var roleCmd = &cobra.Command{
	Use:     "role",
	Aliases: []string{"roles"},
	Short:   "Manage RBAC roles (admin)",
}

// parsePermissions turns repeated --permission resource:action flags into
// the RolePermission slice the API expects.
func parsePermissions(flags []string) ([]kusoApi.RolePermission, error) {
	out := make([]kusoApi.RolePermission, 0, len(flags))
	for _, f := range flags {
		res, act, ok := strings.Cut(f, ":")
		if !ok || res == "" || act == "" {
			return nil, fmt.Errorf("invalid --permission %q: want resource:action (e.g. project:read)", f)
		}
		out = append(out, kusoApi.RolePermission{Resource: res, Action: act})
	}
	return out, nil
}

var roleListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List roles",
	Args:    cobra.NoArgs,
	Example: `  kuso role list
  kuso role list --full -o json | jq '.[].permissions'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRoleList()
	},
}

// runRoleList is shared by `role list` and `get roles`.
func runRoleList() error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	list := api.ListRoles
	if roleFull {
		list = api.ListRolesFull
	}
	resp, err := list()
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("list roles: %w", err)
	}
	var roles []map[string]any
	if err := json.Unmarshal(resp.Body(), &roles); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	sort.Slice(roles, func(i, j int) bool {
		return asString(roles[i]["name"]) < asString(roles[j]["name"])
	})
	switch outputFormat {
	case "json":
		return jsonOut(roles)
	case "table", "":
		t := tablewriter.NewWriter(os.Stdout)
		if roleFull {
			t.SetHeader([]string{"ID", "NAME", "DESCRIPTION", "PERMISSIONS"})
		} else {
			t.SetHeader([]string{"ID", "NAME", "DESCRIPTION"})
		}
		for _, r := range roles {
			row := []string{asString(r["id"]), asString(r["name"]), asString(r["description"])}
			if roleFull {
				row = append(row, permSummary(r["permissions"]))
			}
			t.Append(row)
		}
		t.Render()
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", outputFormat)
	}
}

// permSummary renders the inlined permissions array as a compact
// resource:action, resource:action list for the table view.
func permSummary(v any) string {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(arr))
	for _, p := range arr {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		// The /roles/full endpoint serializes the raw struct (capitalized
		// keys); create/update echo the request shape (lowercase). Accept both.
		res := asString(m["resource"])
		if res == "" {
			res = asString(m["Resource"])
		}
		act := asString(m["action"])
		if act == "" {
			act = asString(m["Action"])
		}
		parts = append(parts, fmt.Sprintf("%s:%s", res, act))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

var roleGetCmd = &cobra.Command{
	Use:     "get <id>",
	Short:   "Show one role with its permissions",
	Args:    cobra.ExactArgs(1),
	Example: `  kuso role get cl9x… -o json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// The API has no single-role GET; fetch the full list and select.
		resp, err := api.ListRolesFull()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("get role: %w", err)
		}
		var roles []map[string]any
		if err := json.Unmarshal(resp.Body(), &roles); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		for _, r := range roles {
			if asString(r["id"]) == args[0] {
				return jsonOut(r)
			}
		}
		return fmt.Errorf("role %q not found", args[0])
	},
}

var roleCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a role",
	Args:  cobra.NoArgs,
	Example: `  kuso role create --name editor --permission project:read --permission project:write
  kuso role create --name viewer --description 'read-only' --permission project:read`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if roleName == "" {
			return fmt.Errorf("--name is required")
		}
		perms, err := parsePermissions(rolePermissions)
		if err != nil {
			return err
		}
		resp, err := api.CreateRole(kusoApi.RoleRequest{
			Name:        roleName,
			Description: roleDescription,
			Permissions: perms,
		})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("create role: %w", err)
		}
		var data map[string]any
		_ = json.Unmarshal(resp.Body(), &data)
		fmt.Printf("role %q created (id=%s, permissions=%d)\n", roleName, asString(data["id"]), len(perms))
		return nil
	},
}

var roleEditCmd = &cobra.Command{
	Use:   "edit <id>",
	Short: "Replace a role's name/description/permissions",
	Long: `Replace a role's name, description, and permission set. The
permission set is REPLACED wholesale — pass every --permission the role
should have, not just additions. Shrinking the set invalidates the JWTs
of users holding the role so the change takes effect immediately.`,
	Args:    cobra.ExactArgs(1),
	Example: `  kuso role edit cl9x… --name editor --permission project:read --permission project:write`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if roleName == "" {
			return fmt.Errorf("--name is required (the update replaces the full role)")
		}
		perms, err := parsePermissions(rolePermissions)
		if err != nil {
			return err
		}
		resp, err := api.UpdateRole(args[0], kusoApi.RoleRequest{
			Name:        roleName,
			Description: roleDescription,
			Permissions: perms,
		})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("edit role: %w", err)
		}
		fmt.Printf("role %s updated (permissions=%d)\n", args[0], len(perms))
		return nil
	},
}

var roleDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"rm", "remove"},
	Short:   "Delete a role",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DeleteRole(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("delete role: %w", err)
		}
		fmt.Printf("role %s deleted\n", args[0])
		return nil
	},
}

// getRolesCmd wires `kuso get roles` — the alias the `user create` and
// `group role` help text points at for discovering a role id.
var getRolesCmd = &cobra.Command{
	Use:     "roles",
	Aliases: []string{"role"},
	Short:   "List roles (alias for `kuso role list`)",
	Args:    cobra.NoArgs,
	Example: `  kuso get roles
  kuso get roles -o json | jq -r '.[] | "\(.id)\t\(.name)"'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRoleList()
	},
}

func init() {
	rootCmd.AddCommand(roleCmd)
	roleCmd.AddCommand(roleListCmd)
	roleCmd.AddCommand(roleGetCmd)
	roleCmd.AddCommand(roleCreateCmd)
	roleCmd.AddCommand(roleEditCmd)
	roleCmd.AddCommand(roleDeleteCmd)

	roleCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	roleListCmd.Flags().BoolVar(&roleFull, "full", false, "include each role's permissions")
	for _, c := range []*cobra.Command{roleCreateCmd, roleEditCmd} {
		c.Flags().StringVar(&roleName, "name", "", "role name")
		c.Flags().StringVar(&roleDescription, "description", "", "role description")
		c.Flags().StringArrayVar(&rolePermissions, "permission", nil, "grant, repeatable: resource:action (e.g. project:read)")
	}

	// `kuso get roles` — registered onto the existing get command so the
	// help references in `user create` / `group role` resolve.
	getCmd.AddCommand(getRolesCmd)
}
