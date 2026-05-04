package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso instance-addon` — register shared database servers that
// projects can carve a per-project DB out of (Model 2). Admin only.
//
// Backed by entries in the kuso-instance-shared kube Secret keyed
// INSTANCE_ADDON_<UPPER>_DSN_ADMIN; the dedicated CLI surface is a
// friendlier alias of `kuso instance-secret set` for that prefix.

var instanceAddonCmd = &cobra.Command{
	Use:     "instance-addon",
	Aliases: []string{"instance-addons", "iaddon"},
	Short:   "Manage instance-wide shared database servers (admin only)",
}

var instanceAddonListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List registered instance addons",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListInstanceAddons()
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var body struct {
			Addons []struct {
				Name string `json:"name"`
				Host string `json:"host"`
				Port string `json:"port"`
				User string `json:"user"`
				Kind string `json:"kind"`
			} `json:"addons"`
		}
		if err := json.Unmarshal(resp.Body(), &body); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(body.Addons)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "KIND", "HOST", "PORT", "USER"})
			for _, a := range body.Addons {
				t.Append([]string{a.Name, a.Kind, a.Host, a.Port, a.User})
			}
			t.Render()
			return nil
		}
	},
}

var instanceAddonRegisterCmd = &cobra.Command{
	Use:   "register <name> <dsn>",
	Short: "Register a shared database server (admin only)",
	Long: `Register a superuser DSN for a shared database server. Projects then
opt in via 'kuso project addon connect-instance <project> <addon> --instance <name>'
or via the canvas + addon → Instance tab in the UI. The DSN must
have CREATE DATABASE + CREATE ROLE privileges.`,
	Example: `  kuso instance-addon register pg 'postgres://admin:pw@shared-pg.example.com:5432/postgres?sslmode=disable'`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req := kusoApi.RegisterInstanceAddonRequest{Name: args[0], DSN: args[1]}
		resp, err := api.RegisterInstanceAddon(req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("instance addon %q registered\n", args[0])
		return nil
	},
}

var instanceAddonUnregisterCmd = &cobra.Command{
	Use:     "unregister <name>",
	Aliases: []string{"rm", "delete"},
	Short:   "Unregister a shared database server",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.UnregisterInstanceAddon(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("instance addon %q unregistered\n", args[0])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(instanceAddonCmd)
	instanceAddonCmd.AddCommand(instanceAddonListCmd)
	instanceAddonListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	instanceAddonCmd.AddCommand(instanceAddonRegisterCmd)
	instanceAddonCmd.AddCommand(instanceAddonUnregisterCmd)
}
