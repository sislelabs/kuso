package kusoCli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// addon.go rounds out the `kuso project addon` subtree to match the web
// UI: enable/disable the public TCP endpoint, reveal connection
// credentials, and update a live addon's spec (version / size / HA /
// storage / database). The add/list/subscribe/delete/connect commands
// live in project.go alongside the rest of the addon subtree.

var (
	addonUpdateVersion     string
	addonUpdateSize        string
	addonUpdateStorageSize string
	addonUpdateDatabase    string
	addonUpdateHA          bool
	addonSecretReveal      bool
	addonSecretKeysOnly    bool
)

// addonPublicTCPCmd groups the public-TCP toggles.
var addonPublicTCPCmd = &cobra.Command{
	Use:     "public-tcp",
	Aliases: []string{"publictcp", "tcp"},
	Short:   "Expose (or hide) an addon on a raw public TCP port",
	Long: `Manage an addon's public TCP endpoint. When enabled, kuso allocates a
port from the cluster's configured pool and binds a Traefik IngressRouteTCP
to the addon's primary Service. The addon's own protocol auth (Postgres
SCRAM, Redis ACL, MinIO keys, …) is the ONLY access gate — kuso adds none.
Admin-only.`,
}

var addonPublicTCPEnableCmd = &cobra.Command{
	Use:   "enable <project> <addon>",
	Short: "Allocate a public TCP port for the addon (admin only)",
	Example: `  kuso project addon public-tcp enable scubatony scubatony-storage-staging
  # then connect at kuso.sislelabs.com:<port> with the addon's own creds`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.EnablePublicTCP(args[0], args[1])
		if err != nil {
			return fmt.Errorf("enable public-tcp: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var out struct {
			Port int `json:"port"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("addon %s/%s exposed on public TCP port %d\n", args[0], args[1], out.Port)
		fmt.Fprintln(cmd.ErrOrStderr(), "note: reachable from the public internet — the addon's own auth is the only gate")
		return nil
	},
}

var addonPublicTCPDisableCmd = &cobra.Command{
	Use:     "disable <project> <addon>",
	Aliases: []string{"rm", "remove"},
	Short:   "Free the addon's public TCP port and remove the route (admin only)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DisablePublicTCP(args[0], args[1])
		if err != nil {
			return fmt.Errorf("disable public-tcp: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("public TCP endpoint removed from %s/%s\n", args[0], args[1])
		return nil
	},
}

var addonSecretCmd = &cobra.Command{
	Use:   "secret <project> <addon>",
	Short: "Show an addon's connection details (keys masked unless --reveal)",
	Long: `Print the addon's connection secret. By default values are masked and
only keys are shown; --reveal prints plaintext (DATABASE_URL, passwords,
access keys, …). Revealing values is admin-gated server-side — the same
boundary as reading env values or opening a shell. --keys lists keys only
(viewer-gated) and never touches the values.`,
	Example: `  kuso project addon secret scubatony scubatony-storage-staging
  kuso project addon secret scubatony scubatony-storage-staging --reveal
  kuso project addon secret scubatony scubatony-storage-staging --keys`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, addon := args[0], args[1]

		// --keys: viewer-gated keys-only listing, never fetches values.
		if addonSecretKeysOnly {
			resp, err := api.AddonSecretKeys(project, addon)
			if err != nil {
				return fmt.Errorf("addon secret keys: %w", err)
			}
			if resp.StatusCode() >= 300 {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
			}
			var out struct {
				Keys []string `json:"keys"`
			}
			if err := json.Unmarshal(resp.Body(), &out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			sort.Strings(out.Keys)
			for _, k := range out.Keys {
				fmt.Println(k)
			}
			return nil
		}

		resp, err := api.AddonSecret(project, addon)
		if err != nil {
			return fmt.Errorf("addon secret: %w", err)
		}
		if resp.StatusCode() == 403 {
			return fmt.Errorf("forbidden: revealing addon connection values requires the admin role (use --keys for names only)")
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var out struct {
			Values map[string]string `json:"values"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if outputFormat == "json" {
			b, _ := json.MarshalIndent(out.Values, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		keys := make([]string, 0, len(out.Values))
		for k := range out.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := out.Values[k]
			if !addonSecretReveal {
				v = maskSecret(v)
			}
			fmt.Printf("%s=%s\n", k, v)
		}
		return nil
	},
}

// maskSecret keeps the value's shape recognisable (length hint) without
// printing it — first 3 chars then a length tag, matching how secret
// previews are surfaced elsewhere.
func maskSecret(v string) string {
	if v == "" {
		return ""
	}
	if len(v) <= 4 {
		return strings.Repeat("•", len(v))
	}
	return v[:3] + fmt.Sprintf("…(%d chars)", len(v))
}

var addonUpdateCmd = &cobra.Command{
	Use:   "update <project> <addon>",
	Short: "Update a live addon's spec (version, size, HA, storage, database)",
	Long: `Patch an addon's spec. Only the flags you pass are changed. Some fields
trigger a rolling restart of the addon pod; storageSize is immutable on most
storage classes (see docs/EDIT_SAFETY.md before resizing).`,
	Example: `  kuso project addon update analiz analiz-postgres --version 17
  kuso project addon update analiz analiz-cache --size medium
  kuso project addon update analiz analiz-postgres --ha`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		var req kusoApi.UpdateAddonRequest
		changed := false
		if cmd.Flags().Changed("version") {
			req.Version = &addonUpdateVersion
			changed = true
		}
		if cmd.Flags().Changed("size") {
			req.Size = &addonUpdateSize
			changed = true
		}
		if cmd.Flags().Changed("storage-size") {
			req.StorageSize = &addonUpdateStorageSize
			changed = true
		}
		if cmd.Flags().Changed("database") {
			req.Database = &addonUpdateDatabase
			changed = true
		}
		if cmd.Flags().Changed("ha") {
			req.HA = &addonUpdateHA
			changed = true
		}
		if !changed {
			return fmt.Errorf("pass at least one of --version --size --storage-size --database --ha")
		}
		resp, err := api.UpdateAddon(args[0], args[1], req)
		if err != nil {
			return fmt.Errorf("update addon: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("addon %s/%s updated\n", args[0], args[1])
		return nil
	},
}

func init() {
	// Hang the new commands off the existing addon subtree defined in
	// project.go (projectAddonCmd is registered there).
	projectAddonCmd.AddCommand(addonPublicTCPCmd)
	addonPublicTCPCmd.AddCommand(addonPublicTCPEnableCmd)
	addonPublicTCPCmd.AddCommand(addonPublicTCPDisableCmd)

	projectAddonCmd.AddCommand(addonSecretCmd)
	addonSecretCmd.Flags().BoolVar(&addonSecretReveal, "reveal", false, "print plaintext values (admin only)")
	addonSecretCmd.Flags().BoolVar(&addonSecretKeysOnly, "keys", false, "list key names only (viewer-gated, never fetches values)")
	addonSecretCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")

	projectAddonCmd.AddCommand(addonUpdateCmd)
	addonUpdateCmd.Flags().StringVar(&addonUpdateVersion, "version", "", "chart/image version")
	addonUpdateCmd.Flags().StringVar(&addonUpdateSize, "size", "", "tier: small|medium|large")
	addonUpdateCmd.Flags().StringVar(&addonUpdateStorageSize, "storage-size", "", "PVC size, e.g. 20Gi (immutable on most storage classes)")
	addonUpdateCmd.Flags().StringVar(&addonUpdateDatabase, "database", "", "default database name")
	addonUpdateCmd.Flags().BoolVar(&addonUpdateHA, "ha", false, "enable high-availability (multi-replica)")
}
