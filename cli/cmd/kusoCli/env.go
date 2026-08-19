package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/go-resty/resty/v2"
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

// envScopeFlag scopes `env set`/`env unset` to a single environment. Empty
// = service-level (the value applies to every env via propagation). Set
// (e.g. "staging") = a per-env override written onto that env's CR, which
// wins over the service-level value for the same key.
var envScopeFlag string

// envSecretFlag routes `env set` writes into the kuso-managed
// <service>-secrets Secret (envFrom-mounted into the pod) as an actual
// secret VALUE, instead of a plaintext literal on the CR. These land as
// source:"managed-secret" entries in `env list`. Service-level only — see
// the guard in envSetCmd (the per-env override path has no secretValue
// wire field).
var envSecretFlag bool

// managedSecretSource mirrors the server's Source tag on env vars
// enumerated from the kuso-managed <service>-secrets envFrom mount
// (kube.KusoEnvVar.Source). Kept in sync with server-go's
// projects.managedSecretSource.
const managedSecretSource = "managed-secret"

// envRevealFlag is bound LOCALLY on envListCmd (NOT a shared package global).
// The CLI has a documented hazard where binding shared globals across many
// commands lets the last init() win and silently mis-render output; keep this
// per-command so `-r` only affects `env list`.
var envRevealFlag bool

// envUnsetYes is bound LOCALLY on envUnsetCmd for the same reason as
// envRevealFlag above — no shared globals for per-command flags.
var envUnsetYes bool

// secretKeyRefName/Key defensively extract valueFrom.secretKeyRef.{name,key}
// out of the loosely-typed valueFrom map. The wire shape is
// {"secretKeyRef":{"name":"<secret>","key":"<KEY>"}}; anything missing or
// mis-shaped yields "" so a malformed entry degrades to a blank rather than
// panicking.
func secretKeyRefName(valueFrom map[string]any) (name, key string) {
	if valueFrom == nil {
		return "", ""
	}
	ref, ok := valueFrom["secretKeyRef"].(map[string]any)
	if !ok {
		return "", ""
	}
	name, _ = ref["name"].(string)
	key, _ = ref["key"].(string)
	return name, key
}

var envListCmd = &cobra.Command{
	Use:     "list <project> <service>",
	Aliases: []string{"ls"},
	Short:   "List a service's env vars, managed secrets, and addon/shared refs",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// --reveal resolves every value to plaintext server-side (admin /
		// secrets:read only — a non-admin still gets masked values back).
		var (
			resp *resty.Response
			err  error
		)
		if envRevealFlag {
			resp, err = api.GetEnvRevealed(args[0], args[1])
		} else {
			resp, err = api.GetEnv(args[0], args[1])
		}
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list env vars: %w", err)
		}
		// Server returns `{envVars: [{name, value, valueFrom, source}], masked,
		// revealed}`. Plain entries have value populated; ref entries have
		// valueFrom (value redacted to empty unless revealed); entries from the
		// kuso-managed <service>-secrets envFrom mount carry
		// source:"managed-secret" (value empty unless revealed). With
		// reveal=true AND admin, `revealed` is true and every value is
		// resolved to plaintext.
		var data struct {
			EnvVars []struct {
				Name      string         `json:"name"`
				Value     string         `json:"value"`
				ValueFrom map[string]any `json:"valueFrom,omitempty"`
				Source    string         `json:"source,omitempty"`
			} `json:"envVars"`
			Masked   bool `json:"masked"`
			Revealed bool `json:"revealed"`
		}
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(data)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "VALUE", "TYPE"})
			sort.Slice(data.EnvVars, func(i, j int) bool { return data.EnvVars[i].Name < data.EnvVars[j].Name })
			// revealed is only true when the server actually resolved values
			// (reveal=true AND admin). A non-admin's reveal request comes back
			// revealed:false, so we correctly fall back to masked rendering.
			revealed := data.Revealed
			for _, e := range data.EnvVars {
				switch {
				case e.Source == managedSecretSource:
					// The kuso managed secret: value lives in the
					// <service>-secrets Secret (envFrom-mounted), off the CR.
					val := "•••••"
					if revealed {
						val = e.Value
					}
					t.Append([]string{e.Name, val, "secret"})
				case e.ValueFrom != nil:
					// An addon/shared reference — valueFrom.secretKeyRef points
					// at another Secret. Show the wiring target when not
					// revealed; the resolved value when revealed.
					name, key := secretKeyRefName(e.ValueFrom)
					val := "→ " + name + "." + key
					if revealed {
						val = e.Value
					}
					t.Append([]string{e.Name, val, "ref"})
				default:
					// A plain literal on the CR.
					t.Append([]string{e.Name, e.Value, "env"})
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

		// --secret: store an actual secret VALUE in the kuso-managed
		// <service>-secrets Secret (envFrom-mounted) via the single-var
		// PUT, sending {"secretValue":"…"}. This is a service-level write
		// only — the per-env override path (--env) has no secretValue wire
		// field, so combining the two would silently drop the secret. Refuse.
		if envSecretFlag {
			if envScopeFlag != "" {
				return fmt.Errorf("--secret cannot be combined with --env (managed-secret values are service-level only)")
			}
			for _, kv := range kvs {
				eq := strings.IndexByte(kv, '=')
				if eq <= 0 {
					return fmt.Errorf("argument %q is not KEY=VALUE", kv)
				}
				val := kv[eq+1:]
				resp, err := api.SetEnvVar(project, service, kv[:eq], kusoApi.SetEnvVarRequest{SecretValue: &val})
				if err := checkRespErr(resp, err); err != nil {
					return err
				}
			}
			fmt.Printf("set %d secret env var(s) on %s/%s [managed-secret]\n", len(kvs), project, service)
			return nil
		}

		// --env: write per-env overrides directly onto ONE env CR. Each
		// KEY=VALUE is an idempotent per-key upsert (the server merges it
		// over the service-level value), so no read-merge-whole-list dance.
		if envScopeFlag != "" {
			for _, kv := range kvs {
				eq := strings.IndexByte(kv, '=')
				if eq <= 0 {
					return fmt.Errorf("argument %q is not KEY=VALUE", kv)
				}
				resp, err := api.SetEnvScopedVar(project, service, envScopeFlag, kv[:eq], kusoApi.EnvVarRequest{Value: kv[eq+1:]})
				if err := checkRespErr(resp, err); err != nil {
					return err
				}
			}
			fmt.Printf("set %d env override(s) on %s/%s [env=%s]\n", len(kvs), project, service, envScopeFlag)
			return nil
		}

		// Default (no --secret, no --env): the UNIFIED "one secret primitive"
		// write. Each KEY=VALUE goes to the single-var PUT with {value, auto:
		// true} and the SERVER decides storage — a ${{ ref }} becomes
		// addon/shared wiring, a build-relevant name stays a CR literal, and
		// everything else becomes a managed secret. The user no longer picks
		// plain-vs-secret. Because each write is an idempotent per-key upsert,
		// there's no read-merge-whole-list dance (and thus no risk of a failed
		// read clobbering untouched vars, and no admin-mask merge hazard).
		for _, kv := range kvs {
			eq := strings.IndexByte(kv, '=')
			if eq <= 0 {
				return fmt.Errorf("argument %q is not KEY=VALUE", kv)
			}
			resp, err := api.SetEnvVar(project, service, kv[:eq], kusoApi.SetEnvVarRequest{
				Value: kv[eq+1:],
				Auto:  true,
			})
			if err := checkRespErr(resp, err); err != nil {
				return err
			}
		}
		fmt.Printf("set %d env var(s) on %s/%s\n", len(kvs), project, service)
		return nil
	},
}

var envUnsetCmd = &cobra.Command{
	Use:   "unset <project> <service> KEY [KEY ...]",
	Short: "Remove plain env var(s) from a service",
	Long: "Remove plain env var(s) from a service.\n\n" +
		"Removing a variable rolls the pods, and an app that reads it at\n" +
		"boot may crash-loop on the new revision. Values are NOT recoverable\n" +
		"from kuso once unset — re-add them from your own records.",
	Args: cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service, keys := args[0], args[1], args[2:]

		// KEY is variadic, so one stray shell word (an unquoted glob, a
		// trailing token) silently removes an extra variable. Echo the
		// exact key list back before doing it, and gate both the
		// --env-scoped and service-level paths below.
		scope := fmt.Sprintf("%s/%s", project, service)
		if envScopeFlag != "" {
			scope += " [env=" + envScopeFlag + "]"
		}
		if err := confirmDestructive(envUnsetYes,
			fmt.Sprintf("Unset %d env var(s) on %s: %s? Pods will roll and the values are not recoverable.",
				len(keys), scope, strings.Join(keys, ", "))); err != nil {
			return err
		}

		// --env: remove per-env overrides directly from ONE env CR.
		if envScopeFlag != "" {
			for _, k := range keys {
				resp, err := api.UnsetEnvScopedVar(project, service, envScopeFlag, k)
				if err != nil {
					return err
				}
				if resp.StatusCode() == 404 {
					return fmt.Errorf("%s is not an override on %s/%s [env=%s]", k, project, service, envScopeFlag)
				}
				if resp.StatusCode() >= 300 {
					return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
				}
			}
			fmt.Printf("unset %d env override(s) on %s/%s [env=%s]\n", len(keys), project, service, envScopeFlag)
			return nil
		}

		// Per-key DELETE for each name. The server's UnsetEnvVar removes
		// ANY form — a CR literal, a secretKeyRef, or a managed-secret key.
		// This replaces the old read-modify-write bulk SetEnv, which only
		// rewrote spec.envVars and therefore could NOT remove a managed
		// secret (its value lives in <service>-secrets, off the CR) —
		// `env unset` reported success but left the secret behind. Per-key
		// deletes are idempotent; a 404 means the key was already gone.
		removed := 0
		for _, k := range keys {
			resp, err := api.DeleteEnvVar(project, service, k)
			if err != nil {
				return err
			}
			switch {
			case resp.StatusCode() < 300:
				removed++
			case resp.StatusCode() == 404:
				// Already absent — not an error, just not counted.
			default:
				return fmt.Errorf("unset %s: server returned %d: %s",
					k, resp.StatusCode(), string(resp.Body()))
			}
		}
		fmt.Printf("unset %d env var(s) on %s/%s\n", removed, project, service)
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
var secretForceFlag bool

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
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list secrets: %w", err)
		}
		var data struct {
			Keys []string `json:"keys"`
			Env  *string  `json:"env"`
		}
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
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
			Force: secretForceFlag,
		})
		if err != nil {
			return err
		}
		if resp.StatusCode() == 409 {
			if s := parseShadowed(resp.Body()); s != nil {
				// Service-scoped writes shadow the project-shared Secret —
				// kube's envFrom mounts service-scoped after shared, so the
				// service value silently overrides. That's usually fine and
				// often intentional (per-service override of a shared
				// default), but requiring --force prevents the user from
				// accidentally diverging two services' values.
				return fmt.Errorf(
					"%s is already set as a project-shared secret on %s\n"+
						"\nthis service-scoped write would override the shared value at pod start.\n"+
						"if that's intentional, fix one of:\n"+
						"  • drop the shared key:  kuso shared-secret unset %s %s\n"+
						"  • or force the write:  kuso secret set %s %s %s … --force\n",
					s.Key, args[0],
					args[0], s.Key,
					args[0], args[1], s.Key,
				)
			}
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

// secretUnsetYes is bound LOCALLY on secretUnsetCmd — no shared globals
// for per-command flags (see envRevealFlag).
var secretUnsetYes bool

var secretUnsetCmd = &cobra.Command{
	Use:   "unset <project> <service> KEY",
	Short: "Remove a secret key from a service (--env to scope to one env)",
	Long: "Remove a secret key from a service (--env to scope to one env).\n\n" +
		"Removing a secret rolls the pods, and an app that reads it at boot\n" +
		"may crash-loop on the new revision. The value is NOT recoverable\n" +
		"from kuso once unset — re-add it from your own records.",
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// Same guard as the twin `env unset` — the value is gone for good
		// and the pods roll.
		scope := "shared"
		if secretEnvFlag != "" {
			scope = secretEnvFlag
		}
		if err := confirmDestructive(secretUnsetYes,
			fmt.Sprintf("Unset secret %s on %s/%s [%s]? Pods will roll and the value is not recoverable.",
				args[2], args[0], args[1], scope)); err != nil {
			return err
		}
		resp, err := api.UnsetSecret(args[0], args[1], args[2], secretEnvFlag)
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		fmt.Printf("secret %s unset on %s/%s [%s]\n", args[2], args[0], args[1], scope)
		return nil
	},
}

// subscriptionShape mirrors the GET /shared-env-keys response.
// LegacyMode was removed in v0.16.11 — server startup migration seeds
// every service with an explicit subscription, so the field is always
// authoritative.
type subscriptionShape struct {
	Subscribed []string `json:"subscribed"`
	Sources    []struct {
		Keys []string `json:"keys"`
	} `json:"sources"`
}

func readSubscription(project, service string) (*subscriptionShape, error) {
	resp, err := api.GetSharedEnvKeys(project, service)
	if err := checkRespErr(resp, err); err != nil {
		return nil, fmt.Errorf("read current subscription: %w", err)
	}
	var out subscriptionShape
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("decode subscription: %w", err)
	}
	return &out, nil
}

var envShareCmd = &cobra.Command{
	Use:   "share <project> <service> KEY [KEY ...]",
	Short: "Subscribe a service to keys from project/instance shared secrets",
	Long: `Subscribe a service to specific keys from the project-shared and instance-shared
secrets. Only subscribed keys reach the pod, so adding a new key to a
shared secret doesn't silently leak into every service.

Examples:
  kuso env share myproj api DATABASE_URL JWT_SECRET
  kuso env share myproj worker DATABASE_URL          # narrow to just one key
  kuso env unshare myproj api JWT_SECRET             # remove a subscription`,
	Args: cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service, addKeys := args[0], args[1], args[2:]
		existing, err := readSubscription(project, service)
		if err != nil {
			return err
		}
		baseline := append([]string{}, existing.Subscribed...)
		seen := map[string]bool{}
		for _, k := range baseline {
			seen[k] = true
		}
		for _, k := range addKeys {
			if !seen[k] {
				seen[k] = true
				baseline = append(baseline, k)
			}
		}
		resp, err := api.SetSharedEnvKeys(project, service, baseline)
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		// Report the server's authoritative resulting subscription, not the
		// locally-computed intent — so a silent revert (or a server-side
		// dedupe) is visible instead of a misleading count.
		fmt.Printf("subscribed %s/%s — now subscribed to %d shared key(s)\n", project, service, serverSharedKeyCount(resp.Body(), len(baseline)))
		return nil
	},
}

// serverSharedKeyCount decodes spec.sharedEnvKeys from a KusoService PUT
// response and returns its length. Falls back to `fallback` if the body
// can't be decoded (older server, non-JSON).
func serverSharedKeyCount(body []byte, fallback int) int {
	var sj struct {
		Spec struct {
			SharedEnvKeys []string `json:"sharedEnvKeys"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &sj); err != nil || sj.Spec.SharedEnvKeys == nil {
		return fallback
	}
	return len(sj.Spec.SharedEnvKeys)
}

var envUnshareCmd = &cobra.Command{
	Use:   "unshare <project> <service> KEY [KEY ...]",
	Short: "Remove keys from a service's shared-secret subscription",
	Args:  cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service, drop := args[0], args[1], args[2:]
		existing, err := readSubscription(project, service)
		if err != nil {
			return err
		}
		dropSet := map[string]bool{}
		for _, k := range drop {
			dropSet[k] = true
		}
		next := existing.Subscribed[:0]
		for _, k := range existing.Subscribed {
			if !dropSet[k] {
				next = append(next, k)
			}
		}
		resp, err := api.SetSharedEnvKeys(project, service, next)
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		fmt.Printf("unsubscribed %s/%s — now subscribed to %d shared key(s)\n", project, service, serverSharedKeyCount(resp.Body(), len(next)))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(envCmd)
	envCmd.AddCommand(envListCmd, envSetCmd, envUnsetCmd, envShareCmd, envUnshareCmd)
	envCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	// --reveal/-r: bound LOCALLY on envListCmd (never a shared global — see the
	// envRevealFlag doc). Asks the server to resolve every value to plaintext;
	// requires the admin/secrets:read role or values stay masked.
	envListCmd.Flags().BoolVarP(&envRevealFlag, "reveal", "r", false, "resolve and print real values (requires secrets:read/admin)")
	// --env on set/unset: write a per-env override instead of a service-level
	// var. Empty keeps the service-level (all-envs) behavior.
	envSetCmd.Flags().StringVar(&envScopeFlag, "env", "", "scope to one environment (e.g. staging); empty = service-level (all envs)")
	// --secret: store the VALUE in the kuso-managed <service>-secrets Secret
	// (envFrom-mounted) instead of as a plaintext literal on the CR.
	envSetCmd.Flags().BoolVar(&envSecretFlag, "secret", false, "store the value as a managed secret in <service>-secrets (envFrom-mounted) instead of a plaintext literal")
	envUnsetCmd.Flags().StringVar(&envScopeFlag, "env", "", "scope to one environment (e.g. staging); empty = service-level (all envs)")
	envUnsetCmd.Flags().BoolVarP(&envUnsetYes, "yes", "y", false, "skip the confirmation prompt")

	rootCmd.AddCommand(secretCmd)
	secretCmd.AddCommand(secretListCmd, secretSetCmd, secretUnsetCmd)
	secretCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	secretCmd.PersistentFlags().StringVar(&secretEnvFlag, "env", "", "scope to one environment (production|preview-pr-N); empty = shared across all envs")
	secretSetCmd.Flags().BoolVar(&secretForceFlag, "force", false, "override the shadow check (set even if a project-shared secret with the same key exists)")
	secretUnsetCmd.Flags().BoolVarP(&secretUnsetYes, "yes", "y", false, "skip the confirmation prompt")
}
