package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso env` — manage plain environment variables on a service.
//
//   kuso env list <project> <service> [-o json]
//   kuso env set <project> <service> KEY=VALUE [KEY2=VALUE2 ...]
//   kuso env unset <project> <service> KEY [KEY2 ...]
//
// `kuso secret` — manage secret-typed env vars (Kubernetes Secret-backed).
//
//   kuso secret list <project> <service>
//   kuso secret set <project> <service> KEY VALUE
//   kuso secret unset <project> <service> KEY
//
// Plain env vars sit on KusoService.spec.envVars and are visible in YAML.
// Secrets live in a per-service Kubernetes Secret and are mounted via
// envFromSecrets — the values never round-trip through the API.

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage plain environment variables on a service",
}

var envListCmd = &cobra.Command{
	Use:     "list <project> <service>",
	Aliases: []string{"ls"},
	Short:   "List a service's plain env vars + the names of its secret keys",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetEnv(args[0], args[1])
		if err != nil {
			return err
		}
		// Server returns `{envVars: [{name, value, valueFrom}]}`. Plain
		// entries have value populated; secret-backed entries have
		// valueFrom + value redacted to empty.
		var data struct {
			EnvVars []struct {
				Name      string         `json:"name"`
				Value     string         `json:"value"`
				ValueFrom map[string]any `json:"valueFrom,omitempty"`
			} `json:"envVars"`
		}
		_ = json.Unmarshal(resp.Body(), &data)
		switch outputFormat {
		case "json":
			return jsonOut(data)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "VALUE", "TYPE"})
			sort.Slice(data.EnvVars, func(i, j int) bool { return data.EnvVars[i].Name < data.EnvVars[j].Name })
			for _, e := range data.EnvVars {
				if e.ValueFrom != nil {
					t.Append([]string{e.Name, "<secret>", "secret"})
				} else {
					t.Append([]string{e.Name, e.Value, "plain"})
				}
			}
			t.Render()
			return nil
		}
	},
}

var envSetCmd = &cobra.Command{
	Use:   "set <project> <service> KEY=VALUE [KEY=VALUE ...]",
	Short: "Set or replace plain env vars on a service",
	Args:  cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service, kvs := args[0], args[1], args[2:]

		// Read current env so we can merge — set should add/update, not replace.
		// Existing valueFrom-backed entries (secret refs) are preserved.
		//
		// Critical: if the read leg fails (typically 401 after a token
		// expiry), we MUST NOT proceed to the write — silently
		// unmarshalling an error body into `existing` produces an
		// empty list, and the subsequent SetEnv would clobber every
		// other env var on the service. The previous code did exactly
		// that, with a `_ = json.Unmarshal(...)` swallowing the error.
		current, err := api.GetEnv(project, service)
		if err != nil {
			return fmt.Errorf("read current env: %w", err)
		}
		if current.StatusCode() >= 300 {
			return fmt.Errorf("read current env: server returned %d: %s",
				current.StatusCode(), string(current.Body()))
		}
		var existing struct {
			EnvVars []map[string]any `json:"envVars"`
		}
		if err := json.Unmarshal(current.Body(), &existing); err != nil {
			return fmt.Errorf("decode current env: %w", err)
		}

		// Build a map for easy update. Preserve valueFrom on existing
		// entries so secret-backed vars survive a plain-var set.
		byName := map[string]map[string]any{}
		for _, e := range existing.EnvVars {
			row := map[string]any{"name": e["name"]}
			if v, ok := e["value"]; ok && v != nil {
				row["value"] = v
			}
			if vf, ok := e["valueFrom"]; ok && vf != nil {
				row["valueFrom"] = vf
			}
			byName[asString(e["name"])] = row
		}
		for _, kv := range kvs {
			eq := -1
			for i, c := range kv {
				if c == '=' {
					eq = i
					break
				}
			}
			if eq <= 0 {
				return fmt.Errorf("argument %q is not KEY=VALUE", kv)
			}
			byName[kv[:eq]] = map[string]any{"name": kv[:eq], "value": kv[eq+1:]}
		}

		out := make([]map[string]any, 0, len(byName))
		for _, v := range byName {
			out = append(out, v)
		}
		resp, err := api.SetEnv(project, service, kusoApi.SetEnvRequest{EnvVars: out})
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("set %d env var(s) on %s/%s\n", len(kvs), project, service)
		return nil
	},
}

var envUnsetCmd = &cobra.Command{
	Use:   "unset <project> <service> KEY [KEY ...]",
	Short: "Remove plain env var(s) from a service",
	Args:  cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service, keys := args[0], args[1], args[2:]
		// Same precaution as set: status-check before unmarshal so a
		// 401 doesn't silently empty the env list and the write doesn't
		// wipe everything.
		current, err := api.GetEnv(project, service)
		if err != nil {
			return fmt.Errorf("read current env: %w", err)
		}
		if current.StatusCode() >= 300 {
			return fmt.Errorf("read current env: server returned %d: %s",
				current.StatusCode(), string(current.Body()))
		}
		var existing struct {
			EnvVars []map[string]any `json:"envVars"`
		}
		if err := json.Unmarshal(current.Body(), &existing); err != nil {
			return fmt.Errorf("decode current env: %w", err)
		}

		drop := map[string]bool{}
		for _, k := range keys {
			drop[k] = true
		}
		out := make([]map[string]any, 0, len(existing.EnvVars))
		for _, e := range existing.EnvVars {
			if drop[asString(e["name"])] {
				continue
			}
			out = append(out, map[string]any{"name": e["name"], "value": e["value"]})
		}
		resp, err := api.SetEnv(project, service, kusoApi.SetEnvRequest{EnvVars: out})
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("unset %d env var(s) on %s/%s\n", len(keys), project, service)
		return nil
	},
}

// ----------------- secrets -----------------
//
// Secrets are mounted into the running pod via envFromSecrets on the
// KusoEnvironment. There are two scopes:
//
//   - shared (default): one Secret per service, mounted on every env.
//   - per-env (--env <name>): a Secret only mounted on that env. Per-env
//     values OVERRIDE shared, since shared is mounted first.
//
// Examples:
//   kuso secret set hello web DATABASE_URL postgres://...
//     # shared — every env gets it (production + every preview)
//   kuso secret set hello web SENTRY_DSN $prodDsn --env production
//     # only the production env sees this
//   kuso secret set hello web FEATURE_X 1 --env preview-pr-42
//     # only the preview-pr-42 env sees this

var secretEnvFlag string

var secretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Manage secret-typed env vars (Kubernetes Secret-backed)",
}

var secretListCmd = &cobra.Command{
	Use:     "list <project> <service>",
	Aliases: []string{"ls"},
	Short:   "List secret keys on a service (values are never returned)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListSecrets(args[0], args[1], secretEnvFlag)
		if err != nil {
			return err
		}
		var data struct {
			Keys []string `json:"keys"`
			Env  *string  `json:"env"`
		}
		_ = json.Unmarshal(resp.Body(), &data)
		sort.Strings(data.Keys)
		switch outputFormat {
		case "json":
			return jsonOut(data)
		default:
			scope := "shared"
			if secretEnvFlag != "" {
				scope = secretEnvFlag
			}
			if len(data.Keys) == 0 {
				fmt.Printf("(no secrets in scope %q)\n", scope)
				return nil
			}
			fmt.Printf("# scope: %s\n", scope)
			for _, k := range data.Keys {
				fmt.Println(k)
			}
			return nil
		}
	},
}

var secretSetCmd = &cobra.Command{
	Use:   "set <project> <service> KEY VALUE",
	Short: "Set or replace a secret value (default scope: shared; --env to scope to one env)",
	Args:  cobra.ExactArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.SetSecret(args[0], args[1], kusoApi.SetSecretRequest{
			Key:   args[2],
			Value: args[3],
			Env:   secretEnvFlag,
		})
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		scope := "shared"
		if secretEnvFlag != "" {
			scope = secretEnvFlag
		}
		fmt.Printf("secret %s set on %s/%s [%s]\n", args[2], args[0], args[1], scope)
		return nil
	},
}

var secretUnsetCmd = &cobra.Command{
	Use:   "unset <project> <service> KEY",
	Short: "Remove a secret key from a service (--env to scope to one env)",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.UnsetSecret(args[0], args[1], args[2], secretEnvFlag)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		scope := "shared"
		if secretEnvFlag != "" {
			scope = secretEnvFlag
		}
		fmt.Printf("secret %s unset on %s/%s [%s]\n", args[2], args[0], args[1], scope)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(envCmd)
	envCmd.AddCommand(envListCmd, envSetCmd, envUnsetCmd)
	envCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")

	rootCmd.AddCommand(secretCmd)
	secretCmd.AddCommand(secretListCmd, secretSetCmd, secretUnsetCmd)
	secretCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	secretCmd.PersistentFlags().StringVar(&secretEnvFlag, "env", "", "scope to one environment (production|preview-pr-N); empty = shared across all envs")
}
