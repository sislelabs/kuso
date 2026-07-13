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

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

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
	Use:   "create",
	Short: "Create a new group",
	Args:  cobra.NoArgs,
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
		if err != nil {
			return fmt.Errorf("create group: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
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
		if err != nil {
			return fmt.Errorf("delete group: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
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
		if err != nil {
			return fmt.Errorf("add member: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("user %s added to group %s\n", args[1], args[0])
		return nil
	},
}

var groupMemberRemoveCmd = &cobra.Command{
	Use:     "rm <id> <userId>",
	Aliases: []string{"remove"},
	Short:   "Remove a user from a group",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RemoveGroupMember(args[0], args[1])
		if err != nil {
			return fmt.Errorf("remove member: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
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
		if err != nil {
			return fmt.Errorf("set instance role: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
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

	groupCmd.AddCommand(groupMemberCmd)
	groupMemberCmd.AddCommand(groupMemberListCmd)
	groupMemberCmd.AddCommand(groupMemberAddCmd)
	groupMemberCmd.AddCommand(groupMemberRemoveCmd)

	groupListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")
	groupMemberListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")
	groupTenancyCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")

	groupCreateCmd.Flags().StringVar(&groupCreateName, "name", "", "group name (required)")
	groupCreateCmd.Flags().StringVar(&groupCreateDescription, "description", "", "group description")

	groupDeleteCmd.Flags().BoolVarP(&groupYes, "yes", "y", false, "skip the confirmation prompt")

	groupRoleCmd.Flags().StringVar(&groupRoleValue, "role", "", "admin|editor|viewer, or empty to clear")
}
