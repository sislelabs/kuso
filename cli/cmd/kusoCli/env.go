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
		var data struct {
			Plain []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"plain"`
			SecretKeys []string `json:"secretKeys"`
		}
		_ = json.Unmarshal(resp.Body(), &data)
		switch outputFormat {
		case "json":
			return jsonOut(data)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "VALUE", "TYPE"})
			sort.Slice(data.Plain, func(i, j int) bool { return data.Plain[i].Name < data.Plain[j].Name })
			for _, e := range data.Plain {
				t.Append([]string{e.Name, e.Value, "plain"})
			}
			sort.Strings(data.SecretKeys)
			for _, k := range data.SecretKeys {
				t.Append([]string{k, "<secret>", "secret"})
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
		current, err := api.GetEnv(project, service)
		if err != nil {
			return err
		}
		var existing struct {
			Plain []map[string]any `json:"plain"`
			// secretKeys are kept as-is; secret-typed entries on the CR
			// are referenced via valueFrom so we have to preserve them.
		}
		_ = json.Unmarshal(current.Body(), &existing)

		// Build a map for easy update.
		byName := map[string]map[string]any{}
		for _, e := range existing.Plain {
			byName[asString(e["name"])] = map[string]any{
				"name":  e["name"],
				"value": e["value"],
			}
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
		current, err := api.GetEnv(project, service)
		if err != nil {
			return err
		}
		var existing struct {
			Plain []map[string]any `json:"plain"`
		}
		_ = json.Unmarshal(current.Body(), &existing)

		drop := map[string]bool{}
		for _, k := range keys {
			drop[k] = true
		}
		out := make([]map[string]any, 0, len(existing.Plain))
		for _, e := range existing.Plain {
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
		resp, err := api.ListSecrets(args[0], args[1])
		if err != nil {
			return err
		}
		var data struct {
			Keys []string `json:"keys"`
		}
		_ = json.Unmarshal(resp.Body(), &data)
		sort.Strings(data.Keys)
		switch outputFormat {
		case "json":
			return jsonOut(data)
		default:
			if len(data.Keys) == 0 {
				fmt.Println("(no secrets)")
				return nil
			}
			for _, k := range data.Keys {
				fmt.Println(k)
			}
			return nil
		}
	},
}

var secretSetCmd = &cobra.Command{
	Use:   "set <project> <service> KEY VALUE",
	Short: "Set or replace a secret value",
	Args:  cobra.ExactArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.SetSecret(args[0], args[1], kusoApi.SetSecretRequest{Key: args[2], Value: args[3]})
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("secret %s set on %s/%s\n", args[2], args[0], args[1])
		return nil
	},
}

var secretUnsetCmd = &cobra.Command{
	Use:   "unset <project> <service> KEY",
	Short: "Remove a secret key from a service",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.UnsetSecret(args[0], args[1], args[2])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("secret %s unset on %s/%s\n", args[2], args[0], args[1])
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
}
