package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso instance-secret` — instance-wide env vars. Admin-only. Auto-
// mounted into every service in every project via envFromSecrets.

var instanceSecretCmd = &cobra.Command{
	Use:     "instance-secret",
	Aliases: []string{"instance-secrets", "isec"},
	Short:   "Manage instance-wide shared secrets (admin only)",
}

var instanceSecretListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List instance secret keys (values are write-only)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListInstanceSecrets()
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

var instanceSecretSetCmd = &cobra.Command{
	Use:   "set <KEY=VALUE>",
	Short: "Upsert an instance secret",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso instance-secret set SENTRY_DSN=https://abc@sentry.io/123
  kuso instance-secret set DATADOG_API_KEY=abc123`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		eq := strings.IndexByte(args[0], '=')
		if eq <= 0 {
			return fmt.Errorf("argument must be KEY=VALUE")
		}
		req := kusoApi.SetSharedSecretRequest{Key: args[0][:eq], Value: args[0][eq+1:]}
		resp, err := api.SetInstanceSecret(req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("instance secret %s set\n", req.Key)
		return nil
	},
}

var instanceSecretUnsetCmd = &cobra.Command{
	Use:     "unset <KEY>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove an instance secret",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.UnsetInstanceSecret(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("instance secret %s removed\n", args[0])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(instanceSecretCmd)
	instanceSecretCmd.AddCommand(instanceSecretListCmd)
	instanceSecretListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	instanceSecretCmd.AddCommand(instanceSecretSetCmd)
	instanceSecretCmd.AddCommand(instanceSecretUnsetCmd)
}
