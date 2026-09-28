// `kuso project grant` — manage per-project RBAC grants (give a user or
// group access at admin|editor|viewer). The server CRUD
// (/api/projects/{p}/grants) was UI-only; this makes project access
// control scriptable / agent-drivable.

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
	grantUser  string
	grantGroup string
	grantRole  string
	grantYes   bool
)

var projectGrantCmd = &cobra.Command{
	Use:     "grant",
	Aliases: []string{"grants"},
	Short:   "Manage per-project access grants (user/group → admin|editor|viewer)",
}

var projectGrantListCmd = &cobra.Command{
	Use:     "list <project>",
	Aliases: []string{"ls"},
	Short:   "List access grants on a project",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListGrants(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list grants: %w", err)
		}
		var env struct {
			Grants []map[string]any `json:"grants"`
		}
		if err := json.Unmarshal(resp.Body(), &env); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(env.Grants)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "KIND", "GRANTEE", "ROLE"})
			for _, g := range env.Grants {
				grantee := asString(g["userId"])
				if grantee == "" {
					grantee = asString(g["groupId"])
				}
				role := asString(g["roleOverride"])
				if role == "" {
					role = "(inherit)"
				}
				t.Append([]string{asString(g["id"]), asString(g["kind"]), grantee, role})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

var projectGrantAddCmd = &cobra.Command{
	Use:   "add <project>",
	Short: "Grant a user or group access to a project",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso project grant add tickero --user alice --role editor
  kuso project grant add tickero --user alice@example.com --role viewer
  kuso project grant add tickero --user 3f2a9c0e8b7d4e1fa6c5b4d3e2f1a0b9 --role editor
  kuso project grant add tickero --group g_devs --role viewer`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if (grantUser == "") == (grantGroup == "") {
			return fmt.Errorf("exactly one of --user or --group is required")
		}
		if grantRole != "" && grantRole != "admin" && grantRole != "editor" && grantRole != "viewer" {
			return fmt.Errorf("invalid --role %q (want admin|editor|viewer, or omit to inherit)", grantRole)
		}
		userID := grantUser
		if grantUser != "" && !isHexUserID(grantUser) {
			uresp, err := api.ListUsers()
			if err := checkRespErr(uresp, err); err != nil {
				return fmt.Errorf("look up user %q: %w (pass the user id from kuso user list instead)", grantUser, err)
			}
			var users []grantUserSummary
			if err := json.Unmarshal(uresp.Body(), &users); err != nil {
				return fmt.Errorf("decode users: %w", err)
			}
			userID, err = resolveGrantUser(grantUser, users)
			if err != nil {
				return err
			}
		}
		resp, err := api.AddGrant(args[0], kusoApi.AddGrantRequest{
			UserID:  userID,
			GroupID: grantGroup,
			Role:    grantRole,
		})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("add grant: %w", err)
		}
		who := grantUser
		if who == "" {
			who = grantGroup
		}
		fmt.Printf("granted %s access to %s (role=%s)\n", who, args[0], orInherit(grantRole))
		return nil
	},
}

var projectGrantRemoveCmd = &cobra.Command{
	Use:     "remove <project> <grant-id>",
	Aliases: []string{"rm"},
	Short:   "Remove an access grant (grant-id from `grant list`)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(grantYes,
			fmt.Sprintf("Remove grant %s from %s (revokes access immediately)?", args[1], args[0])); err != nil {
			return err
		}
		resp, err := api.RemoveGrant(args[0], args[1])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("remove grant: %w", err)
		}
		fmt.Printf("grant %s removed from %s\n", args[1], args[0])
		return nil
	},
}

func orInherit(role string) string {
	if role == "" {
		return "inherit"
	}
	return role
}

func init() {
	projectCmd.AddCommand(projectGrantCmd)
	projectGrantCmd.AddCommand(projectGrantListCmd)
	projectGrantCmd.AddCommand(projectGrantAddCmd)
	projectGrantCmd.AddCommand(projectGrantRemoveCmd)
	projectGrantListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")
	projectGrantAddCmd.Flags().StringVar(&grantUser, "user", "", "user to grant access to: username, email, or user id")
	projectGrantAddCmd.Flags().StringVar(&grantGroup, "group", "", "group id to grant access to")
	projectGrantAddCmd.Flags().StringVar(&grantRole, "role", "", "admin|editor|viewer (omit to inherit instance role)")
	projectGrantRemoveCmd.Flags().BoolVarP(&grantYes, "yes", "y", false, "skip the confirmation prompt")
}

type grantUserSummary struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// isHexUserID reports whether ref already looks like a user id
// (32 lowercase hex chars, as kuso user list prints them).
func isHexUserID(ref string) bool {
	if len(ref) != 32 {
		return false
	}
	for _, r := range ref {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// resolveGrantUser maps a username or email to a user id. Username
// matches exactly; email matches case-insensitively.
func resolveGrantUser(ref string, users []grantUserSummary) (string, error) {
	for _, u := range users {
		if u.Username == ref || (u.Email != "" && strings.EqualFold(u.Email, ref)) || u.ID == ref {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("no user with username or email %q (see kuso user list)", ref)
}
