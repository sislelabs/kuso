// `kuso invite` — admin invite-link management (create / list / revoke).
// Mirrors the admin Invites page. Every subcommand requires
// instance-admin (user:write); you'll get a 403 otherwise.
//
//   kuso invite create [--group <id>] [--role editor] [--expires-in 168h] [--max-uses N] [--note '...']
//   kuso invite list [-o json]
//   kuso invite revoke <id>

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
		if err != nil {
			return fmt.Errorf("create invite: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var data struct {
			Invite map[string]any `json:"invite"`
			URL    string         `json:"url"`
		}
		_ = json.Unmarshal(resp.Body(), &data)
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
					asString(inv["expiresAt"]),
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
		if err != nil {
			return fmt.Errorf("revoke invite: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
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
		resp, err := api.RawGet("/api/invites/lookup/" + args[0])
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
	inviteLookupCmd.Flags().StringVarP(&outputFormat, "output", "o", "json", "output format: json")

	inviteCreateCmd.Flags().StringVar(&inviteCreateGroup, "group", "", "group id to add the invitee to")
	inviteCreateCmd.Flags().StringVar(&inviteCreateRole, "role", "", "instance role on redeem: admin|editor|viewer")
	inviteCreateCmd.Flags().StringVar(&inviteCreateExpiresIn, "expires-in", "", "expiry duration, e.g. 168h (7d); required if --max-uses > 1")
	inviteCreateCmd.Flags().IntVar(&inviteCreateMaxUses, "max-uses", 0, "max redemptions (default 1)")
	inviteCreateCmd.Flags().StringVar(&inviteCreateNote, "note", "", "free-text note")

	inviteListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json")

	inviteRevokeCmd.Flags().BoolVarP(&inviteYes, "yes", "y", false, "skip the confirmation prompt")
}
