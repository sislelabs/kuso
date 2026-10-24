// addon_pooler.go: `kuso project addon pooler enable|disable|set` — the
// opt-in PgBouncer pooler in front of a postgres addon. Each flag maps to
// one pointer field of the PATCH body, so unset flags leave the stored
// value alone.

package kusoCli

import (
	"fmt"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var (
	addonPoolerPoolSize        int32
	addonPoolerExternalBackend string // "on" | "off" | "" (leave alone)
	addonPoolerHost            string
	addonPoolerPort            int32
	addonPoolerDisableYes      bool
)

// addonPoolerPatch builds the pooler block from the flags that were set.
// enabled nil leaves the on/off state alone.
func addonPoolerPatch(cmd *cobra.Command, enabled *bool) (*kusoApi.UpdateAddonPooler, error) {
	changed := cmd.Flags().Changed
	p := &kusoApi.UpdateAddonPooler{Enabled: enabled}
	if changed("pool-size") {
		if addonPoolerPoolSize < 0 {
			return nil, fmt.Errorf("--pool-size must be >= 0 (0 = the default of 25)")
		}
		v := addonPoolerPoolSize
		p.PoolSize = &v
	}
	if changed("external-backend") {
		v, err := parseSetOnOff("external-backend", addonPoolerExternalBackend)
		if err != nil {
			return nil, err
		}
		p.ExternalBackend = &v
	}
	if changed("host") {
		v := addonPoolerHost
		p.Host = &v
	}
	if changed("port") {
		if addonPoolerPort < 0 || addonPoolerPort > 65535 {
			return nil, fmt.Errorf("--port must be 0-65535 (got %d)", addonPoolerPort)
		}
		v := addonPoolerPort
		p.Port = &v
	}
	if enabled == nil && p.PoolSize == nil && p.ExternalBackend == nil && p.Host == nil && p.Port == nil {
		return nil, fmt.Errorf("nothing to change: pass --pool-size, --external-backend, --host or --port")
	}
	return p, nil
}

func addPoolerSettingFlags(cmd *cobra.Command) {
	cmd.Flags().Int32Var(&addonPoolerPoolSize, "pool-size", 0, "PgBouncer default_pool_size: backend connections per user/db (0 = default 25)")
	cmd.Flags().StringVar(&addonPoolerExternalBackend, "external-backend", "", "pool an external managed postgres instead of the in-cluster pod (on|off)")
	cmd.Flags().StringVar(&addonPoolerHost, "host", "", "external backend host (with --external-backend=on)")
	cmd.Flags().Int32Var(&addonPoolerPort, "port", 0, "external backend port (default 5432)")
}

func runAddonPoolerPatch(project, addon string, p *kusoApi.UpdateAddonPooler, msg string) error {
	resp, err := api.UpdateAddon(project, addon, kusoApi.UpdateAddonRequest{Pooler: p})
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("update pooler: %w", err)
	}
	fmt.Printf("addon %s/%s: %s\n", project, addon, msg)
	return nil
}

var addonPoolerCmd = &cobra.Command{
	Use:   "pooler",
	Short: "Manage the PgBouncer pooler in front of a postgres addon",
	Long: `The pooler is an opt-in PgBouncer in front of a postgres addon. Services
reach it through ${{ <addon>.POOLER_URL }}; DATABASE_URL keeps pointing at
the database itself.

Use --external-backend=on with --host / --port to pool an external managed
postgres (connect-external) when the provider caps connections. Keep
--pool-size below what the provider leaves you: PlanetScale's 25-slot plan
spends ~13 on its own roles, so 6 is safe there. The pooler needs
POSTGRES_USER and POSTGRES_PASSWORD in the addon's conn secret.`,
}

var addonPoolerEnableCmd = &cobra.Command{
	Use:   "enable <project> <addon>",
	Short: "Turn the pooler on (optionally setting its options)",
	Example: `  kuso project addon pooler enable shop shop-db
  kuso project addon pooler enable shop shop-db --pool-size 10
  kuso project addon pooler enable tickero psdb --external-backend=on --host host.psdb.cloud --port 5432 --pool-size 6`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		p, err := addonPoolerPatch(cmd, kusoApi.BoolPtr(true))
		if err != nil {
			return err
		}
		return runAddonPoolerPatch(args[0], args[1], p, "pooler enabled (use ${{ <addon>.POOLER_URL }} to connect through it)")
	},
}

var addonPoolerSetCmd = &cobra.Command{
	Use:   "set <project> <addon>",
	Short: "Change pooler options without turning it on or off",
	Example: `  kuso project addon pooler set shop shop-db --pool-size 20
  kuso project addon pooler set tickero psdb --host new-host.psdb.cloud`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		p, err := addonPoolerPatch(cmd, nil)
		if err != nil {
			return err
		}
		return runAddonPoolerPatch(args[0], args[1], p, "pooler updated")
	},
}

var addonPoolerDisableCmd = &cobra.Command{
	Use:   "disable <project> <addon>",
	Short: "Turn the pooler off (services using POOLER_URL lose their connection)",
	Long: `Turn the pooler off. Its Service goes away, so any service connecting
through ${{ <addon>.POOLER_URL }} can no longer reach the database until
it is switched to DATABASE_URL. Pool settings are kept for a later enable.

Prompts for confirmation unless --yes.`,
	Example: `  kuso project addon pooler disable shop shop-db
  kuso project addon pooler disable shop shop-db --yes`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(addonPoolerDisableYes, fmt.Sprintf(
			"Disable the pooler on %s/%s? Services using POOLER_URL lose their database connection.", args[0], args[1])); err != nil {
			return err
		}
		return runAddonPoolerPatch(args[0], args[1], &kusoApi.UpdateAddonPooler{Enabled: kusoApi.BoolPtr(false)}, "pooler disabled")
	},
}

func init() {
	projectAddonCmd.AddCommand(addonPoolerCmd)
	addonPoolerCmd.AddCommand(addonPoolerEnableCmd, addonPoolerSetCmd, addonPoolerDisableCmd)
	addPoolerSettingFlags(addonPoolerEnableCmd)
	addPoolerSettingFlags(addonPoolerSetCmd)
	addonPoolerDisableCmd.Flags().BoolVarP(&addonPoolerDisableYes, "yes", "y", false, "skip the confirmation prompt")
}
