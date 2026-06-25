// `kuso user` — admin user management (list / create / delete /
// set-password / instance-role). Mirrors the admin Users page. Every
// subcommand requires instance-admin (user:write) on the server; you'll
// get a 403 otherwise.
//
//   kuso user list [-o json]
//   kuso user create --username alice --email alice@x.io --password '...'
//   kuso user delete <id>
//   kuso user set-password <id> --password '...'
//   kuso user role <id> --role editor   (or --role '' to clear)

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
	userCreateUsername  string
	userCreateEmail     string
	userCreatePassword  string
	userCreateFirstName string
	userCreateLastName  string
	userCreateRoleID    string
	userSetPassword     string
	userRoleValue       string
	userYes             bool
)

var userCmd = &cobra.Command{
	Use:     "user",
	Aliases: []string{"users"},
	Short:   "Manage users (admin)",
}

var userListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List users",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListUsers()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		var users []map[string]any
		if err := json.Unmarshal(resp.Body(), &users); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(users)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "USERNAME", "EMAIL", "ROLE", "INSTANCE ROLE", "ACTIVE"})
			for _, u := range users {
				t.Append([]string{
					asString(u["id"]),
					asString(u["username"]),
					asString(u["email"]),
					asString(u["role"]),
					asString(u["instanceRole"]),
					asString(u["isActive"]),
				})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

var userCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new user",
	Args:  cobra.NoArgs,
	Example: `  kuso user create --username alice --email alice@example.com --password 's3cret!!'
  kuso user create --username bob --email bob@x.io --password 'pw' --role-id <roleId>`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if userCreateUsername == "" || userCreateEmail == "" || userCreatePassword == "" {
			return fmt.Errorf("--username, --email, and --password are required")
		}
		resp, err := api.CreateUser(kusoApi.CreateUserRequest{
			Username:  userCreateUsername,
			Email:     userCreateEmail,
			Password:  userCreatePassword,
			FirstName: userCreateFirstName,
			LastName:  userCreateLastName,
			RoleID:    userCreateRoleID,
		})
		if err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var data map[string]any
		_ = json.Unmarshal(resp.Body(), &data)
		fmt.Printf("user %q created (id=%s)\n", userCreateUsername, asString(data["id"]))
		return nil
	},
}

var userDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"rm"},
	Short:   "Delete a user",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(userYes,
			fmt.Sprintf("Delete user %s (revokes their access immediately)?", args[0])); err != nil {
			return err
		}
		resp, err := api.DeleteUser(args[0])
		if err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("user %s deleted\n", args[0])
		return nil
	},
}

var userSetPasswordCmd = &cobra.Command{
	Use:   "set-password <id>",
	Short: "Set a user's password (admin; logs the user out of all sessions)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if userSetPassword == "" {
			return fmt.Errorf("--password is required")
		}
		resp, err := api.SetUserPassword(args[0], userSetPassword)
		if err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("password updated for user %s\n", args[0])
		return nil
	},
}

var userRoleCmd = &cobra.Command{
	Use:   "role <id>",
	Short: "Set a user's instance role (admin|editor|viewer, or empty to clear)",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso user role <id> --role editor
  kuso user role <id> --role ''   # clear (inherit from groups)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if userRoleValue != "" && userRoleValue != "admin" && userRoleValue != "editor" && userRoleValue != "viewer" {
			return fmt.Errorf("invalid --role %q (want admin|editor|viewer, or '' to clear)", userRoleValue)
		}
		resp, err := api.SetUserInstanceRole(args[0], kusoApi.SetUserInstanceRoleRequest{Role: userRoleValue})
		if err != nil {
			return fmt.Errorf("set instance role: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		role := userRoleValue
		if role == "" {
			role = "(cleared)"
		}
		fmt.Printf("instance role for user %s set to %s\n", args[0], role)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(userCmd)
	userCmd.AddCommand(userListCmd)
	userCmd.AddCommand(userCreateCmd)
	userCmd.AddCommand(userDeleteCmd)
	userCmd.AddCommand(userSetPasswordCmd)
	userCmd.AddCommand(userRoleCmd)

	userListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")

	userCreateCmd.Flags().StringVar(&userCreateUsername, "username", "", "username (required)")
	userCreateCmd.Flags().StringVar(&userCreateEmail, "email", "", "email (required)")
	userCreateCmd.Flags().StringVar(&userCreatePassword, "password", "", "password (required)")
	userCreateCmd.Flags().StringVar(&userCreateFirstName, "first-name", "", "first name")
	userCreateCmd.Flags().StringVar(&userCreateLastName, "last-name", "", "last name")
	userCreateCmd.Flags().StringVar(&userCreateRoleID, "role-id", "", "role id (from `kuso get roles`/admin)")

	userDeleteCmd.Flags().BoolVarP(&userYes, "yes", "y", false, "skip the confirmation prompt")

	userSetPasswordCmd.Flags().StringVar(&userSetPassword, "password", "", "new password (required)")

	userRoleCmd.Flags().StringVar(&userRoleValue, "role", "", "admin|editor|viewer, or empty to clear")
}
