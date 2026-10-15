// `kuso group` — admin group / RBAC management. Mirrors the admin
// Groups page: CRUD, membership, instance-role, and project-tenancy.
// Every subcommand requires instance-admin (user:write); `member list`
// requires settings:admin specifically. You'll get a 403 otherwise.
//
//   kuso group list [-o json]
//   kuso group create --name devs [--description '...']
//   kuso group delete <id>
//   kuso group member list <id> [-o json]
//   kuso group member add <id> <userId>
//   kuso group member rm  <id> <userId>
//   kuso group role <id> --role editor   (or --role '' to clear)
//   kuso group tenancy <id> [-o json]    (read tenancy)
//   kuso group tenancy set <id> --project p=role … [--instance-role r]
//   kuso group update <id> [--name n] [--description d]

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"kuso/pkg/kusoApi"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var (
	groupCreateName        string
	groupCreateDescription string
	groupRoleValue         string
	groupYes               bool
)

var groupCmd = &cobra.Command{
	Use:     "group",
	Aliases: []string{"groups"},
	Short:   "Manage groups and RBAC (admin)",
}

var groupListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List groups",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListGroups()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list groups: %w", err)
		}
		var groups []map[string]any
		if err := json.Unmarshal(resp.Body(), &groups); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(groups)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "NAME", "DESCRIPTION"})
			for _, g := range groups {
				t.Append([]string{asString(g["id"]), asString(g["name"]), asString(g["description"])})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

var groupCreateCmd = &cobra.Command{
	Use:     "create",
	Short:   "Create a new group",
	Args:    cobra.NoArgs,
	Example: `  kuso group create --name devs --description 'engineering team'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if groupCreateName == "" {
			return fmt.Errorf("--name is required")
		}
		resp, err := api.CreateGroup(kusoApi.GroupRequest{
			Name:        groupCreateName,
			Description: groupCreateDescription,
		})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("create group: %w", err)
		}
		var data map[string]any
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("group %q created (id=%s)\n", groupCreateName, asString(data["id"]))
		return nil
	},
}

var groupDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"rm"},
	Short:   "Delete a group",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(groupYes,
			fmt.Sprintf("Delete group %s (affects every member's access)?", args[0])); err != nil {
			return err
		}
		resp, err := api.DeleteGroup(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("delete group: %w", err)
		}
		fmt.Printf("group %s deleted\n", args[0])
		return nil
	},
}

var groupMemberCmd = &cobra.Command{
	Use:   "member",
	Short: "Manage group membership",
}

var groupMemberListCmd = &cobra.Command{
	Use:     "list <id>",
	Aliases: []string{"ls"},
	Short:   "List members of a group",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListGroupMembers(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list group members: %w", err)
		}
		var env struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(resp.Body(), &env); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(env.Data)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "USERNAME", "EMAIL"})
			for _, m := range env.Data {
				t.Append([]string{asString(m["id"]), asString(m["username"]), asString(m["email"])})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

var groupMemberAddCmd = &cobra.Command{
	Use:   "add <id> <userId>",
	Short: "Add a user to a group",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.AddGroupMember(args[0], args[1])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("add member: %w", err)
		}
		fmt.Printf("user %s added to group %s\n", args[1], args[0])
		return nil
	},
}

var groupMemberRemoveYes bool

var groupMemberRemoveCmd = &cobra.Command{
	Use:     "rm <id> <userId>",
	Aliases: []string{"remove"},
	Short:   "Remove a user from a group",
	Long: "Remove a user from a group.\n\n" +
		"They immediately lose the group's project grants and instance role;\n" +
		"re-add them with 'kuso group member add' to restore access.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// Same guard as the twin `grant remove` — an access change that
		// takes effect on the user's next request.
		if err := confirmDestructive(groupMemberRemoveYes,
			fmt.Sprintf("Remove user %s from group %s? They immediately lose the group's grants and role.",
				args[1], args[0])); err != nil {
			return err
		}
		resp, err := api.RemoveGroupMember(args[0], args[1])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("remove member: %w", err)
		}
		fmt.Printf("user %s removed from group %s\n", args[1], args[0])
		return nil
	},
}

var groupRoleCmd = &cobra.Command{
	Use:   "role <id>",
	Short: "Set a group's instance role (admin|editor|viewer, or empty to clear)",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso group role <id> --role editor
  kuso group role <id> --role ''   # clear`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if groupRoleValue != "" && groupRoleValue != "admin" && groupRoleValue != "editor" && groupRoleValue != "viewer" {
			return fmt.Errorf("invalid --role %q (want admin|editor|viewer, or '' to clear)", groupRoleValue)
		}
		resp, err := api.SetGroupInstanceRole(args[0], kusoApi.SetGroupInstanceRoleRequest{Role: groupRoleValue})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("set instance role: %w", err)
		}
		role := groupRoleValue
		if role == "" {
			role = "(cleared)"
		}
		fmt.Printf("instance role for group %s set to %s\n", args[0], role)
		return nil
	},
}

var groupTenancyCmd = &cobra.Command{
	Use:   "tenancy <id>",
	Short: "Show a group's tenancy (instance role + project memberships)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetGroupTenancy(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("get tenancy: %w", err)
		}
		var ten kusoApi.GroupTenancy
		if err := json.Unmarshal(resp.Body(), &ten); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(ten)
		case "table", "":
			fmt.Printf("instance role: %s\n", orNone(ten.InstanceRole))
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"PROJECT", "ROLE"})
			for _, m := range ten.ProjectMemberships {
				t.Append([]string{m.Project, m.Role})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

var (
	groupUpdateName        string
	groupUpdateDescription string
)

var groupUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Rename a group or change its description",
	Example: `  kuso group update <id> --name platform
  kuso group update <id> --description 'on-call rotation'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if !cmd.Flags().Changed("name") && !cmd.Flags().Changed("description") {
			return fmt.Errorf("pass --name and/or --description")
		}
		// PUT replaces both fields and requires name, so fill in the
		// one the caller didn't pass from the current group.
		name, desc := groupUpdateName, groupUpdateDescription
		if !cmd.Flags().Changed("name") || !cmd.Flags().Changed("description") {
			resp, err := api.ListGroups()
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("read group: %w", err)
			}
			var groups []map[string]any
			if err := json.Unmarshal(resp.Body(), &groups); err != nil {
				return fmt.Errorf("decode groups: %w", err)
			}
			found := false
			for _, g := range groups {
				if asString(g["id"]) != args[0] {
					continue
				}
				found = true
				if !cmd.Flags().Changed("name") {
					name = asString(g["name"])
				}
				if !cmd.Flags().Changed("description") {
					desc = asString(g["description"])
				}
			}
			if !found {
				return fmt.Errorf("group %s not found (list ids with 'kuso group list')", args[0])
			}
		}
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("--name can't be empty")
		}
		resp, err := api.UpdateGroup(args[0], kusoApi.GroupRequest{Name: name, Description: desc})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("update group: %w", err)
		}
		fmt.Printf("group %s updated (name=%q)\n", args[0], name)
		return nil
	},
}

var (
	groupTenancySetProjects     []string
	groupTenancySetInstanceRole string
	groupTenancySetClear        bool
)

// parseProjectRoles turns repeated project=role flags into memberships.
func parseProjectRoles(in []string) ([]kusoApi.ProjectMembership, error) {
	out := make([]kusoApi.ProjectMembership, 0, len(in))
	seen := map[string]bool{}
	for _, kv := range in {
		p, r, ok := strings.Cut(kv, "=")
		p, r = strings.TrimSpace(p), strings.TrimSpace(r)
		if !ok || p == "" {
			return nil, fmt.Errorf("--project %q: want PROJECT=ROLE", kv)
		}
		if r != "admin" && r != "editor" && r != "viewer" {
			return nil, fmt.Errorf("--project %q: role must be admin, editor or viewer", kv)
		}
		if seen[p] {
			return nil, fmt.Errorf("--project: %q given twice", p)
		}
		seen[p] = true
		out = append(out, kusoApi.ProjectMembership{Project: p, Role: r})
	}
	return out, nil
}

var groupTenancySetCmd = &cobra.Command{
	Use:   "set <id>",
	Short: "Replace a group's per-project roles (and optionally its instance role)",
	Long: `Replace the group's project memberships with exactly the --project
entries given; projects you leave out lose the group's access. Pass
--clear to remove every project membership. The instance role is kept
unless --instance-role is passed ('' clears it). Members pick the change
up within about a minute, without logging in again.`,
	Example: `  kuso group tenancy set <id> --project shop=editor --project blog=viewer
  kuso group tenancy set <id> --project shop=admin --instance-role viewer
  kuso group tenancy set <id> --clear`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if len(groupTenancySetProjects) == 0 && !groupTenancySetClear && !cmd.Flags().Changed("instance-role") {
			return fmt.Errorf("pass --project PROJECT=ROLE (repeatable), --clear, or --instance-role")
		}
		if len(groupTenancySetProjects) > 0 && groupTenancySetClear {
			return fmt.Errorf("pass --project or --clear, not both")
		}
		r := groupTenancySetInstanceRole
		if r != "" && r != "admin" && r != "editor" && r != "viewer" {
			return fmt.Errorf("invalid --instance-role %q (want admin|editor|viewer, or '' to clear)", r)
		}
		resp, err := api.GetGroupTenancy(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("read tenancy: %w", err)
		}
		var ten kusoApi.GroupTenancy
		if err := json.Unmarshal(resp.Body(), &ten); err != nil {
			return fmt.Errorf("decode tenancy: %w", err)
		}
		if cmd.Flags().Changed("instance-role") {
			ten.InstanceRole = r
		}
		if groupTenancySetClear {
			ten.ProjectMemberships = []kusoApi.ProjectMembership{}
		} else if len(groupTenancySetProjects) > 0 {
			ms, err := parseProjectRoles(groupTenancySetProjects)
			if err != nil {
				return err
			}
			ten.ProjectMemberships = ms
		}
		if ten.ProjectMemberships == nil {
			ten.ProjectMemberships = []kusoApi.ProjectMembership{}
		}
		resp, err = api.SetGroupTenancy(args[0], ten)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("set tenancy: %w", err)
		}
		fmt.Printf("group %s: instance role %s, %d project membership(s)\n", args[0], orNone(ten.InstanceRole), len(ten.ProjectMemberships))
		return nil
	},
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func init() {
	rootCmd.AddCommand(groupCmd)
	groupCmd.AddCommand(groupListCmd)
	groupCmd.AddCommand(groupCreateCmd)
	groupCmd.AddCommand(groupDeleteCmd)
	groupCmd.AddCommand(groupRoleCmd)
	groupCmd.AddCommand(groupTenancyCmd)
	groupCmd.AddCommand(groupUpdateCmd)
	groupTenancyCmd.AddCommand(groupTenancySetCmd)
	groupUpdateCmd.Flags().StringVar(&groupUpdateName, "name", "", "new group name")
	groupUpdateCmd.Flags().StringVar(&groupUpdateDescription, "description", "", "new description ('' clears it)")
	groupTenancySetCmd.Flags().StringArrayVar(&groupTenancySetProjects, "project", nil, "project membership PROJECT=ROLE, role admin|editor|viewer (repeatable; replaces the list)")
	groupTenancySetCmd.Flags().StringVar(&groupTenancySetInstanceRole, "instance-role", "", "instance role admin|editor|viewer, or '' to clear (default: keep)")
	groupTenancySetCmd.Flags().BoolVar(&groupTenancySetClear, "clear", false, "remove every project membership")

	groupCmd.AddCommand(groupMemberCmd)
	groupMemberCmd.AddCommand(groupMemberListCmd)
	groupMemberCmd.AddCommand(groupMemberAddCmd)
	groupMemberRemoveCmd.Flags().BoolVarP(&groupMemberRemoveYes, "yes", "y", false, "skip the confirmation prompt")
	groupMemberCmd.AddCommand(groupMemberRemoveCmd)

	groupListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")
	groupMemberListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")
	groupTenancyCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")

	groupCreateCmd.Flags().StringVar(&groupCreateName, "name", "", "group name (required)")
	groupCreateCmd.Flags().StringVar(&groupCreateDescription, "description", "", "group description")

	groupDeleteCmd.Flags().BoolVarP(&groupYes, "yes", "y", false, "skip the confirmation prompt")

	groupRoleCmd.Flags().StringVar(&groupRoleValue, "role", "", "admin|editor|viewer, or empty to clear")
}
