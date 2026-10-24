// user_get_set.go — admin view/edit of one user, and changing your own
// password:
//
//	kuso user get <id> [-o json]
//	kuso user set <id> [--email] [--first-name] [--last-name] [--active=true|false] [--role-id id]
//	kuso user profile set-password [--stdin]

package kusoCli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"kuso/pkg/kusoApi"
)

var userGetOutput string

var userGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one user (admin)",
	Example: `  kuso user get clx0abc123
  kuso user get clx0abc123 -o json | jq .isActive`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetUser(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		var u map[string]any
		if err := json.Unmarshal(resp.Body(), &u); err != nil {
			return fmt.Errorf("decode user: %w", err)
		}
		switch userGetOutput {
		case "json":
			delete(u, "image") // a base64 data URL; noise in a terminal or a jq pipe
			return jsonOut(u)
		case "table", "":
		default:
			return fmt.Errorf("unsupported output format %q", userGetOutput)
		}
		name := strings.TrimSpace(asString(u["firstName"]) + " " + asString(u["lastName"]))
		fmt.Printf("id:        %s\n", asString(u["id"]))
		fmt.Printf("username:  %s\n", asString(u["username"]))
		fmt.Printf("email:     %s\n", asString(u["email"]))
		fmt.Printf("name:      %s\n", dashIfEmpty(name))
		fmt.Printf("active:    %v\n", u["isActive"])
		fmt.Printf("role id:   %s\n", dashIfEmpty(asString(u["roleId"])))
		fmt.Printf("provider:  %s\n", dashIfEmpty(asString(u["provider"])))
		return nil
	},
}

var userSet struct {
	email, firstName, lastName, roleID string
	active                             bool
}

// buildUpdateUser sends only the flags the caller passed; the server
// leaves nil fields untouched.
func buildUpdateUser(cmd *cobra.Command) (kusoApi.UpdateUserRequest, bool) {
	var req kusoApi.UpdateUserRequest
	changed := false
	str := func(flag string, v string, dst **string) {
		if cmd.Flags().Changed(flag) {
			s := v
			*dst = &s
			changed = true
		}
	}
	str("email", userSet.email, &req.Email)
	str("first-name", userSet.firstName, &req.FirstName)
	str("last-name", userSet.lastName, &req.LastName)
	str("role-id", userSet.roleID, &req.RoleID)
	if cmd.Flags().Changed("active") {
		a := userSet.active
		req.IsActive = &a
		changed = true
	}
	return req, changed
}

var userSetCmd = &cobra.Command{
	Use:   "set <id>",
	Short: "Edit a user's email, name, active flag or legacy role (admin)",
	Long: `Edit another user's account. Only the flags you pass change.
--active=false deactivates the account and logs it out everywhere; a
--role-id change also logs it out. You can't change your own active flag
or role here. For the instance role use 'kuso user role'.`,
	Example: `  kuso user set clx0abc123 --email ada@example.com
  kuso user set clx0abc123 --first-name Ada --last-name Lovelace
  kuso user set clx0abc123 --active=false`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req, changed := buildUpdateUser(cmd)
		if !changed {
			return fmt.Errorf("pass at least one of --email, --first-name, --last-name, --active, --role-id")
		}
		resp, err := api.UpdateUser(args[0], req)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("update user: %w", err)
		}
		fmt.Printf("user %s updated\n", args[0])
		return nil
	},
}

var userProfileSetPasswordStdin bool

// readPasswordChange returns (current, new). With --stdin it reads two
// lines: the current password, then the new one. Otherwise it prompts
// on the terminal without echo and asks for the new one twice.
func readPasswordChange(fromStdin bool) (string, string, error) {
	if fromStdin {
		sc := bufio.NewScanner(secretStdin)
		var lines []string
		for sc.Scan() && len(lines) < 2 {
			lines = append(lines, strings.TrimSuffix(sc.Text(), "\r"))
		}
		if err := sc.Err(); err != nil {
			return "", "", fmt.Errorf("read stdin: %w", err)
		}
		if len(lines) < 2 {
			return "", "", fmt.Errorf("--stdin wants two lines: the current password, then the new one")
		}
		return lines[0], lines[1], nil
	}
	if !stdinIsTTYFn() {
		return "", "", fmt.Errorf("no terminal to prompt on; pipe the current and new password as two lines with --stdin")
	}
	read := func(label string) (string, error) {
		fmt.Fprint(os.Stderr, label)
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	cur, err := read("Current password: ")
	if err != nil {
		return "", "", err
	}
	next, err := read("New password: ")
	if err != nil {
		return "", "", err
	}
	again, err := read("Repeat new password: ")
	if err != nil {
		return "", "", err
	}
	if next != again {
		return "", "", fmt.Errorf("new passwords don't match")
	}
	return cur, next, nil
}

var userProfileSetPasswordCmd = &cobra.Command{
	Use:   "set-password",
	Short: "Change your own password (logs you out everywhere)",
	Long: `Change your own password. Prompts for the current and new password
on the terminal; for scripts, pipe them as two lines with --stdin. The new
password needs at least 8 characters.

On success the server revokes every token you hold, including this CLI's
session and API tokens you created, so run 'kuso login' afterwards.
Accounts that sign in with GitHub/OAuth only have no password to change.`,
	Example: `  kuso user profile set-password
  printf '%s\n%s\n' "$OLD" "$NEW" | kuso user profile set-password --stdin`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		cur, next, err := readPasswordChange(userProfileSetPasswordStdin)
		if err != nil {
			return err
		}
		if cur == "" {
			return fmt.Errorf("current password is empty")
		}
		if len(next) < 8 {
			return fmt.Errorf("new password must be at least 8 characters")
		}
		resp, err := api.ChangeMyPassword(cur, next)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("change password: %w", err)
		}
		fmt.Println("password changed; every session and token you held is revoked. Run 'kuso login' to sign in again.")
		return nil
	},
}

func init() {
	userCmd.AddCommand(userGetCmd, userSetCmd)
	userProfileCmd.AddCommand(userProfileSetPasswordCmd)
	userGetCmd.Flags().StringVarP(&userGetOutput, "output", "o", "table", "output format [table, json]")
	f := userSetCmd.Flags()
	f.StringVar(&userSet.email, "email", "", "new email address")
	f.StringVar(&userSet.firstName, "first-name", "", "new first name")
	f.StringVar(&userSet.lastName, "last-name", "", "new last name")
	f.BoolVar(&userSet.active, "active", true, "--active=false deactivates the account, --active reactivates it")
	f.StringVar(&userSet.roleID, "role-id", "", "legacy role id ('' clears; list them with kuso get roles)")
	userProfileSetPasswordCmd.Flags().BoolVar(&userProfileSetPasswordStdin, "stdin", false, "read the current and new password as two lines from stdin")
}
