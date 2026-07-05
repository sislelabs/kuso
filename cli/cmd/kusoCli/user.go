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

	profileSetEmail     string
	profileSetFirstName string
	profileSetLastName  string
	profileSetUsername  string
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

// `kuso user profile` — read the current (authenticated) user's own
// profile via GET /api/users/profile. Available to any logged-in user
// (not admin-gated, unlike the rest of the `user` group). JSON out.
var userProfileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Show your own profile (the authenticated user)",
	Args:  cobra.NoArgs,
	Example: `  kuso user profile
  kuso user profile -o json | jq '.email'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RawGet("/api/users/profile")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("get profile: %w", err)
		}
		var data map[string]any
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return jsonOut(data)
	},
}

// `kuso user profile set` — edit your own first/last name + email via
// PUT /api/users/profile. Read-modify-write: fetch the current profile,
// overlay only the flags you pass, PUT it back. The server endpoint
// only accepts firstName/lastName/email; username is admin-managed and
// NOT editable here (passing --username errors).
var userProfileSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Update your own first name, last name, and/or email",
	Args:  cobra.NoArgs,
	Example: `  kuso user profile set --first-name Ada --last-name Lovelace
  kuso user profile set --email ada@example.com`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// The PUT /api/users/profile endpoint only edits firstName /
		// lastName / email — username is not in its request shape and is
		// admin-managed. Fail loudly rather than silently dropping it.
		if cmd.Flags().Changed("username") {
			return fmt.Errorf("--username is not editable via profile (server endpoint ignores it); ask an admin")
		}
		if !cmd.Flags().Changed("email") &&
			!cmd.Flags().Changed("first-name") &&
			!cmd.Flags().Changed("last-name") {
			return fmt.Errorf("pass at least one of --email, --first-name, --last-name")
		}

		// Read current profile so we only overlay the passed flags.
		resp, err := api.RawGet("/api/users/profile")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("read current profile: %w", err)
		}
		var cur struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
			Email     string `json:"email"`
		}
		if err := json.Unmarshal(resp.Body(), &cur); err != nil {
			return fmt.Errorf("decode current profile: %w", err)
		}

		firstName := cur.FirstName
		lastName := cur.LastName
		email := cur.Email
		if cmd.Flags().Changed("first-name") {
			firstName = profileSetFirstName
		}
		if cmd.Flags().Changed("last-name") {
			lastName = profileSetLastName
		}
		if cmd.Flags().Changed("email") {
			email = profileSetEmail
		}

		// Body matches server-go's updateUserRequest (firstName /
		// lastName / email; roleId + isActive are stripped server-side).
		body, err := json.Marshal(map[string]string{
			"firstName": firstName,
			"lastName":  lastName,
			"email":     email,
		})
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		put, err := api.RawPut("/api/users/profile", body, "application/json")
		if err := checkRespErr(put, err); err != nil {
			return fmt.Errorf("update profile: %w", err)
		}
		fmt.Println("profile updated")
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
	userCmd.AddCommand(userProfileCmd)
	userProfileCmd.AddCommand(userProfileSetCmd)

	userListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")

	userProfileCmd.Flags().StringVarP(&outputFormat, "output", "o", "json", "output format: json")
	userProfileSetCmd.Flags().StringVar(&profileSetEmail, "email", "", "new email address")
	userProfileSetCmd.Flags().StringVar(&profileSetFirstName, "first-name", "", "new first name")
	userProfileSetCmd.Flags().StringVar(&profileSetLastName, "last-name", "", "new last name")
	userProfileSetCmd.Flags().StringVar(&profileSetUsername, "username", "", "username (NOT editable via profile; admin-managed)")

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
