package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso shared-secret` — project-level env vars auto-mounted into
// every service. Use case: cross-service integrations like Resend,
// Postmark, Stripe — set once, every service in the project picks
// it up via envFromSecrets.
//
//   kuso shared-secret list <project>
//   kuso shared-secret set <project> <KEY>=<VALUE>
//   kuso shared-secret unset <project> <KEY>

var sharedSecretCmd = &cobra.Command{
	Use:     "shared-secret",
	Aliases: []string{"shared-secrets", "ssec"},
	Short:   "Manage project-level shared secrets (env vars attached to every service)",
}

var sharedSecretListCmd = &cobra.Command{
	Use:     "list <project>",
	Aliases: []string{"ls"},
	Short:   "List shared secret keys (values are write-only and never returned)",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListSharedSecrets(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var body struct {
			Keys []string `json:"keys"`
		}
		if err := json.Unmarshal(resp.Body(), &body); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(body.Keys)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"KEY"})
			for _, k := range body.Keys {
				t.Append([]string{k})
			}
			t.Render()
			return nil
		}
	},
}

var sharedSecretSetCmd = &cobra.Command{
	Use:   "set <project> <KEY=VALUE>",
	Short: "Upsert a shared secret",
	Args:  cobra.ExactArgs(2),
	Example: `  kuso shared-secret set myproj RESEND_API_KEY=re_abc123
  kuso shared-secret set myproj STRIPE_SECRET_KEY=sk_live_xxx`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// Split on the FIRST = so values containing = work (e.g.
		// base64-encoded keys like "AKIA…====").
		kv := args[1]
		eq := -1
		for i, c := range kv {
			if c == '=' {
				eq = i
				break
			}
		}
		if eq <= 0 {
			return fmt.Errorf("argument must be KEY=VALUE")
		}
		req := kusoApi.SetSharedSecretRequest{Key: kv[:eq], Value: kv[eq+1:]}
		resp, err := api.SetSharedSecret(args[0], req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("set %s on %s\n", req.Key, args[0])
		return nil
	},
}

var sharedSecretUnsetCmd = &cobra.Command{
	Use:     "unset <project> <KEY>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove a shared secret",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.UnsetSharedSecret(args[0], args[1])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("unset %s on %s\n", args[1], args[0])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(sharedSecretCmd)
	sharedSecretCmd.AddCommand(sharedSecretListCmd)
	sharedSecretListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	sharedSecretCmd.AddCommand(sharedSecretSetCmd)
	sharedSecretCmd.AddCommand(sharedSecretUnsetCmd)
}
