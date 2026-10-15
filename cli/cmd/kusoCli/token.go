package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso token` — create / list / revoke long-lived API tokens.
//
// Use case: CI / scripts that shouldn't embed your password. The token
// is a JWT signed by the kuso server with your permissions. Subsequent
// `kuso login --token <jwt>` skips username/password.
//
//   kuso token create --name 'github actions' --expires 90d
//   kuso token list [-o json]
//   kuso token revoke <id>
//
// Admin (user:write) variants: `list --all`, `create --user <id>`,
// `revoke --any`.

var tokenCmd = &cobra.Command{
	Use:     "token",
	Aliases: []string{"tokens"},
	Short:   "Create and manage long-lived API tokens",
}

var (
	tokenCreateName    string
	tokenCreateExpires string
	tokenCreateUser    string
	tokenRevokeYes     bool
	tokenRevokeAny     bool
	tokenListAll       bool
)

var tokenCreateCmd = &cobra.Command{
	Use:   "create",
	Args:  cobra.NoArgs,
	Short: "Create a new API token. Prints the token ONCE — save it.",
	Long: `Create a long-lived API token for yourself, or with --user for
another user (admin). The token carries that user's permissions and is
capped at one year.`,
	Example: `  kuso token create --name 'github-actions' --expires 90d
  kuso token create --name ci --expires 2027-01-01
  kuso token create --name deploy-bot --user clx0abc123`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if tokenCreateName == "" {
			return fmt.Errorf("--name is required")
		}
		exp, err := parseExpires(tokenCreateExpires)
		if err != nil {
			return err
		}
		req := kusoApi.CreateTokenRequest{
			Name:      tokenCreateName,
			ExpiresAt: exp.Format(time.RFC3339),
		}
		var resp *resty.Response
		if tokenCreateUser != "" {
			resp, err = api.CreateTokenForUser(tokenCreateUser, req)
		} else {
			resp, err = api.CreateToken(req)
		}
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		var data struct {
			Name      string `json:"name"`
			Token     string `json:"token"`
			ExpiresAt string `json:"expiresAt"`
		}
		// The token is shown exactly once — a swallowed decode error
		// here would print an empty token and lose it forever.
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("Token %q created. Expires %s.\n\n", data.Name, data.ExpiresAt)
		fmt.Println("  --- This token is shown ONCE. Save it now. ---")
		fmt.Println()
		fmt.Println(data.Token)
		fmt.Println()
		fmt.Println("Use it with:  kuso login --api <url> --token '<the token>'")
		return nil
	},
}

var tokenListCmd = &cobra.Command{
	Use:     "list",
	Args:    cobra.NoArgs,
	Aliases: []string{"ls"},
	Short:   "List your API tokens (without the token values)",
	Example: `  kuso token list
  kuso token list --all        # every user's tokens (admin)
  kuso token list --all -o json | jq '.[] | select(.user.username=="ci")'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if tokenListAll {
			return listAllTokens()
		}
		resp, err := api.ListTokens()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list tokens: %w", err)
		}
		var items []map[string]any
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(items)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			// No LAST USED column: the server never records token use
			// (the Token."lastUsed" column is never written), so it was
			// always blank.
			t.SetHeader([]string{"ID", "NAME", "CREATED", "EXPIRES"})
			for _, tok := range items {
				t.Append([]string{
					asString(tok["id"]),
					asString(tok["name"]),
					asString(tok["createdAt"]),
					asString(tok["expiresAt"]),
				})
			}
			t.Render()
			return nil
		}
	},
}

var tokenRevokeCmd = &cobra.Command{
	Use:     "revoke <id>",
	Aliases: []string{"delete", "rm"},
	Short:   "Revoke an API token",
	Long: "Revoke an API token.\n\n" +
		"Takes effect immediately: any CI job, script, or agent still\n" +
		"presenting the token starts getting 401s on its next request.\n" +
		"Revocation cannot be undone — issue a new token with\n" +
		"'kuso token create' if you revoke the wrong one.\n\n" +
		"--any revokes another user's token by id (admin; ids from\n" +
		"'kuso token list --all').",
	Example: `  kuso token revoke 3f9c2a
  kuso token revoke 3f9c2a --any --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// Revoking is instant and irreversible, and the id alone gives
		// no hint about which integration it belongs to — a mistyped
		// argument silently breaks a live pipeline.
		if err := confirmDestructive(tokenRevokeYes,
			fmt.Sprintf("Revoke API token %s? Anything still using it will start failing with 401.", args[0])); err != nil {
			return err
		}
		var resp *resty.Response
		var err error
		if tokenRevokeAny {
			resp, err = api.DeleteAnyToken(args[0])
		} else {
			resp, err = api.DeleteToken(args[0])
		}
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		fmt.Printf("token %s revoked\n", args[0])
		return nil
	},
}

// listAllTokens renders GET /api/tokens: every user's tokens.
func listAllTokens() error {
	resp, err := api.ListAllTokens()
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("list all tokens: %w", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(resp.Body(), &items); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if outputFormat == "json" {
		return jsonOut(items)
	}
	t := tablewriter.NewWriter(os.Stdout)
	t.SetHeader([]string{"ID", "USER", "NAME", "ACTIVE", "CREATED", "EXPIRES"})
	for _, tok := range items {
		user, _ := tok["user"].(map[string]any)
		t.Append([]string{
			asString(tok["id"]),
			asString(user["username"]),
			asString(tok["name"]),
			boolText(tok["isActive"]),
			asString(tok["createdAt"]),
			asString(tok["expiresAt"]),
		})
	}
	t.Render()
	return nil
}

// parseExpires accepts either an absolute RFC3339 / YYYY-MM-DD date or a
// relative "Nd" / "Nh" duration ("90d", "12h"). Returns the absolute
// expiry time.
func parseExpires(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("--expires is required (e.g. 90d, 2026-12-31)")
	}
	// "<n>d" -> n days from now.
	n := len(s)
	if n > 1 && (s[n-1] == 'd' || s[n-1] == 'D') {
		var days int
		if _, err := fmt.Sscanf(s[:n-1], "%d", &days); err == nil && days > 0 {
			return time.Now().Add(time.Duration(days) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(d), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("could not parse expires: %q (try 90d, 2026-12-31, or RFC3339)", s)
}

func init() {
	rootCmd.AddCommand(tokenCmd)
	tokenCmd.AddCommand(tokenCreateCmd)
	tokenCreateCmd.Flags().StringVar(&tokenCreateName, "name", "", "human-readable name for the token (required)")
	tokenCreateCmd.Flags().StringVar(&tokenCreateExpires, "expires", "90d", "expires after: 90d, 12h, 2026-12-31, or RFC3339")
	// --expires-at accepted as a synonym so older docs / muscle memory
	// keep working. When both are set the last-parsed flag wins, which
	// for cobra means whichever the user typed second.
	tokenCreateCmd.Flags().StringVar(&tokenCreateExpires, "expires-at", "90d", "alias for --expires")
	tokenCmd.AddCommand(tokenListCmd)
	tokenListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	tokenCmd.AddCommand(tokenRevokeCmd)
	tokenRevokeCmd.Flags().BoolVarP(&tokenRevokeYes, "yes", "y", false, "skip the confirmation prompt")
	tokenRevokeCmd.Flags().BoolVar(&tokenRevokeAny, "any", false, "revoke any user's token by id (admin)")
	tokenListCmd.Flags().BoolVar(&tokenListAll, "all", false, "list every user's tokens (admin)")
	tokenCreateCmd.Flags().StringVar(&tokenCreateUser, "user", "", "issue the token for this user id instead of yourself (admin)")
}
