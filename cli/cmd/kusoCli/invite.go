// `kuso invite` — admin invite-link management (create / list / revoke).
// Mirrors the admin Invites page. Every subcommand requires
// instance-admin (user:write); you'll get a 403 otherwise.
//
//   kuso invite create [--group <id>] [--role editor] [--expires-in 168h] [--max-uses N] [--note '...']
//   kuso invite list [-o json]
//   kuso invite revoke <id>
//   kuso invite accept <token> --username u --email e [--password-stdin] [--api url]   (no login needed)

package kusoCli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"

	"kuso/pkg/kusoApi"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var (
	inviteCreateGroup     string
	inviteCreateRole      string
	inviteCreateExpiresIn string
	inviteCreateMaxUses   int
	inviteCreateNote      string
	inviteYes             bool
)

var inviteCmd = &cobra.Command{
	Use:     "invite",
	Aliases: []string{"invites"},
	Short:   "Manage invite links (admin)",
}

var inviteCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Mint a fresh invite link",
	Args:  cobra.NoArgs,
	Example: `  kuso invite create --role editor --expires-in 168h
  kuso invite create --group <groupId> --max-uses 5 --expires-in 720h --note 'design team'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if inviteCreateRole != "" && inviteCreateRole != "admin" && inviteCreateRole != "editor" && inviteCreateRole != "viewer" {
			return fmt.Errorf("invalid --role %q (want admin|editor|viewer)", inviteCreateRole)
		}
		// Multi-use invites require an expiry server-side; surface it
		// early rather than as a 400.
		if inviteCreateMaxUses > 1 && inviteCreateExpiresIn == "" {
			return fmt.Errorf("--expires-in is required when --max-uses > 1")
		}
		resp, err := api.CreateInvite(kusoApi.CreateInviteRequest{
			GroupID:      inviteCreateGroup,
			InstanceRole: inviteCreateRole,
			ExpiresIn:    inviteCreateExpiresIn,
			MaxUses:      inviteCreateMaxUses,
			Note:         inviteCreateNote,
		})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("create invite: %w", err)
		}
		var data struct {
			Invite map[string]any `json:"invite"`
			URL    string         `json:"url"`
		}
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("invite %s created\n\n", asString(data.Invite["id"]))
		fmt.Println(data.URL)
		return nil
	},
}

var inviteListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List invites (newest-first)",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListInvites()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list invites: %w", err)
		}
		var invites []map[string]any
		if err := json.Unmarshal(resp.Body(), &invites); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(invites)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "MAX USES", "USED", "EXPIRES", "URL"})
			for _, inv := range invites {
				t.Append([]string{
					asString(inv["id"]),
					asString(inv["maxUses"]),
					asString(inv["usedCount"]),
					inviteExpiry(inv["expiresAt"]),
					asString(inv["url"]),
				})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

var inviteRevokeCmd = &cobra.Command{
	Use:     "revoke <id>",
	Aliases: []string{"delete", "rm"},
	Short:   "Revoke an invite",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(inviteYes,
			fmt.Sprintf("Revoke invite %s?", args[0])); err != nil {
			return err
		}
		resp, err := api.RevokeInvite(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("revoke invite: %w", err)
		}
		fmt.Printf("invite %s revoked\n", args[0])
		return nil
	},
}

// `kuso invite lookup <token>` — resolve an invite token to its public
// summary via GET /api/invites/lookup/{token}. This is the read the
// signup page uses; it's a public endpoint (no admin needed) and only
// exposes display-safe fields (group name, instance role, uses-left,
// expiry, note). Returns 404 if the token is unknown, 410 if it's been
// revoked / expired / used up. JSON out.
var inviteLookupCmd = &cobra.Command{
	Use:   "lookup <token>",
	Short: "Resolve an invite token to its details (public read)",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso invite lookup <token>
  kuso invite lookup <token> -o json | jq '.usesLeft'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RawGet("/api/invites/lookup/" + url.PathEscape(args[0]))
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("lookup invite: %w", err)
		}
		var data map[string]any
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return jsonOut(data)
	},
}

func init() {
	rootCmd.AddCommand(inviteCmd)
	inviteCmd.AddCommand(inviteCreateCmd)
	inviteCmd.AddCommand(inviteListCmd)
	inviteCmd.AddCommand(inviteRevokeCmd)
	inviteCmd.AddCommand(inviteLookupCmd)
	inviteLookupCmd.Flags().StringVarP(&outputFormatJSONOnly, "output", "o", "json", "output format: json")

	inviteCreateCmd.Flags().StringVar(&inviteCreateGroup, "group", "", "group id to add the invitee to")
	inviteCreateCmd.Flags().StringVar(&inviteCreateRole, "role", "", "instance role on redeem: admin|editor|viewer")
	inviteCreateCmd.Flags().StringVar(&inviteCreateExpiresIn, "expires-in", "", "expiry duration, e.g. 168h (7d); required if --max-uses > 1")
	inviteCreateCmd.Flags().IntVar(&inviteCreateMaxUses, "max-uses", 0, "max redemptions (default 1)")
	inviteCreateCmd.Flags().StringVar(&inviteCreateNote, "note", "", "free-text note")

	inviteListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")

	inviteRevokeCmd.Flags().BoolVarP(&inviteYes, "yes", "y", false, "skip the confirmation prompt")
}

// inviteExpiry renders expiresAt, which the server sends as a raw
// sql.NullTime object ({"Time": ..., "Valid": bool}).
func inviteExpiry(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return asString(v)
	}
	if valid, _ := m["Valid"].(bool); !valid {
		return "never"
	}
	return asString(m["Time"])
}

var inviteAccept struct {
	api, username, email string
	passwordStdin        bool
}

// readNewPassword takes one line from stdin with --password-stdin,
// otherwise prompts twice on the terminal without echo.
func readNewPassword(fromStdin bool) (string, error) {
	if fromStdin {
		sc := bufio.NewScanner(secretStdin)
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return "", fmt.Errorf("read stdin: %w", err)
			}
			return "", fmt.Errorf("no password on stdin")
		}
		return strings.TrimSuffix(sc.Text(), "\r"), nil
	}
	if !stdinIsTTYFn() {
		return "", fmt.Errorf("no terminal to prompt on; pipe the password with --password-stdin")
	}
	fmt.Fprint(os.Stderr, "Password: ")
	a, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", fmt.Errorf("passwords don't match")
	}
	return string(a), nil
}

var inviteAcceptCmd = &cobra.Command{
	Use:   "accept <token>",
	Short: "Accept an invite by creating a local username/password account",
	Long: `Redeem an invite token by creating a local account, the same as the
signup page's username/password form. Doesn't need a login: pass the
instance with --api (default: the current instance). The password needs
at least 8 characters; usernames are lowercase letters, digits, '_' and
'-'. To join with GitHub/OAuth instead, open the invite link in a browser.

This doesn't touch your saved CLI session. Sign in as the new account
afterwards with 'kuso login --api <url> -u <username>'.`,
	Example: `  kuso invite accept 9f3c… --api https://kuso.example.com --username ada --email ada@example.com
  printf '%s\n' "$PW" | kuso invite accept 9f3c… --username ada --email ada@example.com --password-stdin`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		f := &inviteAccept
		base := f.api
		if base == "" && api != nil {
			base = api.BaseURL()
		}
		if base == "" {
			return fmt.Errorf("no instance: pass --api https://kuso.example.com")
		}
		if f.username == "" || f.email == "" {
			return fmt.Errorf("--username and --email are required")
		}
		pw, err := readNewPassword(f.passwordStdin)
		if err != nil {
			return err
		}
		if len(pw) < 8 {
			return fmt.Errorf("password must be at least 8 characters")
		}
		// A fresh client with no bearer: the redeem route is public, and
		// the caller's own session must not be attached to it.
		anon := &kusoApi.KusoClient{}
		anon.Init(base, "")
		resp, err := anon.RedeemInviteLocal(args[0], f.username, f.email, pw)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("accept invite: %w", err)
		}
		fmt.Printf("account %q created. Sign in with: kuso login --api %s -u %s\n", f.username, base, f.username)
		return nil
	},
}

func init() {
	inviteCmd.AddCommand(inviteAcceptCmd)
	af := inviteAcceptCmd.Flags()
	af.StringVar(&inviteAccept.api, "api", "", "kuso instance URL (default: the current instance)")
	af.StringVar(&inviteAccept.username, "username", "", "username for the new account (required)")
	af.StringVar(&inviteAccept.email, "email", "", "email for the new account (required)")
	af.BoolVar(&inviteAccept.passwordStdin, "password-stdin", false, "read the password from stdin instead of prompting")
}
