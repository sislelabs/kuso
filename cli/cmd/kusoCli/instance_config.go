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

// ---------- admin settings blobs: build + session ----------
//
// These are separate from the Kuso-CR "settings" above: they're the
// admin knobs persisted to the Setting table and exposed at
// GET/PUT /api/admin/settings/{build,session}. Both are admin-only and
// return a flat JSON object. `set` is a read-modify-write of key=value
// pairs (values JSON-parsed when possible), mirroring `instance-config
// set` — so you only overwrite the keys you name.

// runSettingsGet is shared by build-settings/session-settings get.
func runSettingsGet(path string) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	resp, err := api.RawGet(path)
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("get %s: %w", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return jsonOut(out)
}

// runSettingsSet fetches the current blob at path, overlays the
// key=value args (JSON-parsed, else raw string), and PUTs it back.
func runSettingsSet(path string, args []string) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	resp, err := api.RawGet(path)
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("read current settings: %w", err)
	}
	cur := map[string]any{}
	if err := json.Unmarshal(resp.Body(), &cur); err != nil {
		return fmt.Errorf("decode current settings: %w", err)
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
		var val any
		if err := json.Unmarshal([]byte(raw), &val); err != nil {
			val = raw
		}
		cur[key] = val
	}
	body, err := json.Marshal(cur)
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	putResp, err := api.RawPut(path, body, "application/json")
	if err != nil {
		return fmt.Errorf("put settings: %w", err)
	}
	if putResp.StatusCode() >= 300 {
		return fmt.Errorf("server returned %d: %s", putResp.StatusCode(), string(putResp.Body()))
	}
	fmt.Printf("settings updated (%d key(s))\n", len(args))
	return nil
}

var buildSettingsCmd = &cobra.Command{
	Use:   "build-settings",
	Short: "Read/write the instance build settings (admin)",
}

var buildSettingsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show build settings (concurrency + kaniko resources + registry override)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSettingsGet("/api/admin/settings/build")
	},
}

var buildSettingsSetCmd = &cobra.Command{
	Use:   "set <key>=<value> [<key>=<value> ...]",
	Short: "Set build-settings keys (read-modify-write)",
	Args:  cobra.MinimumNArgs(1),
	Example: `  kuso instance-config build-settings set maxConcurrent=2
  kuso instance-config build-settings set memoryLimit=4Gi cpuLimit=2000m`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSettingsSet("/api/admin/settings/build", args)
	},
}

var sessionSettingsCmd = &cobra.Command{
	Use:   "session-settings",
	Short: "Read/write the instance session settings (admin)",
}

var sessionSettingsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show session settings (login token TTL + never-expire toggle)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSettingsGet("/api/admin/settings/session")
	},
}

var sessionSettingsSetCmd = &cobra.Command{
	Use:   "set <key>=<value> [<key>=<value> ...]",
	Short: "Set session-settings keys (read-modify-write)",
	Args:  cobra.MinimumNArgs(1),
	Example: `  kuso instance-config session-settings set ttlSeconds=2592000
  kuso instance-config session-settings set neverExpire=true`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSettingsSet("/api/admin/settings/session", args)
	},
}

func init() {
	rootCmd.AddCommand(instanceConfigCmd)
	instanceConfigCmd.AddCommand(instanceConfigGetCmd)
	instanceConfigCmd.AddCommand(instanceConfigSetCmd)
	instanceConfigGetCmd.Flags().StringVarP(&outputFormat, "output", "o", "json", "output format: json")

	instanceConfigCmd.AddCommand(buildSettingsCmd)
	buildSettingsCmd.AddCommand(buildSettingsGetCmd)
	buildSettingsCmd.AddCommand(buildSettingsSetCmd)

	instanceConfigCmd.AddCommand(sessionSettingsCmd)
	sessionSettingsCmd.AddCommand(sessionSettingsGetCmd)
	sessionSettingsCmd.AddCommand(sessionSettingsSetCmd)
}
