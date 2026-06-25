// `kuso instance-config` — read/write the instance-wide Kuso CR spec
// ("settings"). Mirrors GET/POST /api/config. `get` is readable by any
// authenticated user; `set` requires settings:admin (you'll get a 403
// otherwise).
//
//   kuso instance-config get [-o json]
//   kuso instance-config set <key>=<value> [<key>=<value> ...]
//
// `set` is read-modify-write: it fetches the current settings, applies
// the supplied top-level key=value pairs (values JSON-parsed when
// possible, else treated as a string), and POSTs the merged object back
// — the server replaces the whole settings blob, so untouched keys are
// preserved by the merge.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var instanceConfigCmd = &cobra.Command{
	Use:     "instance-config",
	Aliases: []string{"iconfig"},
	Short:   "Read/write the instance config (Kuso CR settings)",
}

var instanceConfigGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the instance config settings",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetInstanceConfig()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("get instance config: %w", err)
		}
		var env struct {
			Settings map[string]any `json:"settings"`
		}
		if err := json.Unmarshal(resp.Body(), &env); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if env.Settings == nil {
			env.Settings = map[string]any{}
		}
		// Always pretty-print the settings object — it's nested config,
		// not a flat table.
		return jsonOut(env.Settings)
	},
}

var instanceConfigSetCmd = &cobra.Command{
	Use:   "set <key>=<value> [<key>=<value> ...]",
	Short: "Set top-level instance config keys (read-modify-write)",
	Args:  cobra.MinimumNArgs(1),
	Example: `  kuso instance-config set clusterissuer=letsencrypt-staging
  kuso instance-config set 'registry={"enabled":true}'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// Read current settings so we preserve untouched keys (POST
		// replaces the whole settings object).
		resp, err := api.GetInstanceConfig()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("read current config: %w", err)
		}
		var env struct {
			Settings map[string]any `json:"settings"`
		}
		if err := json.Unmarshal(resp.Body(), &env); err != nil {
			return fmt.Errorf("decode current config: %w", err)
		}
		settings := env.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		for _, kv := range args {
			i := strings.IndexByte(kv, '=')
			if i < 0 {
				return fmt.Errorf("invalid pair %q (want key=value)", kv)
			}
			key := kv[:i]
			raw := kv[i+1:]
			if key == "" {
				return fmt.Errorf("invalid pair %q (empty key)", kv)
			}
			// Try to parse the value as JSON (numbers, bools, objects,
			// arrays); fall back to the raw string when it isn't valid
			// JSON so `clusterissuer=letsencrypt-prod` works unquoted.
			var val any
			if err := json.Unmarshal([]byte(raw), &val); err != nil {
				val = raw
			}
			settings[key] = val
		}
		setResp, err := api.SetInstanceConfig(settings)
		if err != nil {
			return fmt.Errorf("set instance config: %w", err)
		}
		if setResp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", setResp.StatusCode(), string(setResp.Body()))
		}
		fmt.Printf("instance config updated (%d key(s))\n", len(args))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(instanceConfigCmd)
	instanceConfigCmd.AddCommand(instanceConfigGetCmd)
	instanceConfigCmd.AddCommand(instanceConfigSetCmd)
	instanceConfigGetCmd.Flags().StringVarP(&outputFormat, "output", "o", "json", "output format: json")
}
