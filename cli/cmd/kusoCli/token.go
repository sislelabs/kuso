package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

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

var tokenCmd = &cobra.Command{
	Use:     "token",
	Aliases: []string{"tokens"},
	Short:   "Create and manage long-lived API tokens",
}

var (
	tokenCreateName    string
	tokenCreateExpires string
)

var tokenCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new API token. Prints the token ONCE — save it.",
	Example: `  kuso token create --name 'github-actions' --expires 90d
  kuso token create --name ci --expires 2027-01-01`,
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
		resp, err := api.CreateToken(kusoApi.CreateTokenRequest{
			Name:      tokenCreateName,
			ExpiresAt: exp.Format(time.RFC3339),
		})
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var data struct {
			Name      string `json:"name"`
			Token     string `json:"token"`
			ExpiresAt string `json:"expiresAt"`
		}
		_ = json.Unmarshal(resp.Body(), &data)
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
	Aliases: []string{"ls"},
	Short:   "List your API tokens (without the token values)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListTokens()
		if err != nil {
			return err
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
			t.SetHeader([]string{"ID", "NAME", "EXPIRES", "LAST USED"})
			for _, tok := range items {
				t.Append([]string{
					asString(tok["id"]),
					asString(tok["name"]),
					asString(tok["expiresAt"]),
					asString(tok["lastUsed"]),
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
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DeleteToken(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("token %s revoked\n", args[0])
		return nil
	},
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
}
