// environment_domains.go adds the env-scoped domain subtree:
//
//	kuso environment domain add <project> <service> <env> <host>
//	kuso environment domain rm  <project> <service> <env> <host>
//	kuso environment domain set <project> <service> <env> [host...]
//
// These bind extra DNS names to ONE environment's additionalHosts
// (per-env routing) rather than the service as a whole — the wire
// surface behind the dashboard's per-env Networking section. add/rm
// are incremental; set replaces the whole list (no args = clear all).
//
// Registered on environmentCmd (defined in environment.go) via this
// file's own init().

package kusoCli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var environmentDomainCmd = &cobra.Command{
	Use:     "domain",
	Aliases: []string{"domains"},
	Short:   "Manage an environment's additional hostnames (per-env routing)",
	Long: `Bind extra DNS names to ONE environment (staging, preview-pr-N, …) instead
of the service as a whole. A host may only be claimed by one env in the
project at a time — adding a host already routed elsewhere returns 409.`,
}

var envDomainAddTLSSecret string

var environmentDomainAddCmd = &cobra.Command{
	Use:     "add <project> <service> <env> <host>",
	Short:   "Add one hostname to an environment's additionalHosts",
	Example: `  kuso environment domain add scubatony api staging api-staging.example.com`,
	Args:    cobra.ExactArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service, env, host := args[0], args[1], args[2], args[3]
		resp, err := api.AddEnvDomain(project, service, env, host, envDomainAddTLSSecret)
		if err != nil {
			return fmt.Errorf("add env domain: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("added %s to %s/%s env %s\n", host, project, service, env)
		return nil
	},
}

// envDomainRmYes / envDomainSetYes — bound locally, one per command.
var (
	envDomainRmYes  bool
	envDomainSetYes bool
)

var environmentDomainRmCmd = &cobra.Command{
	Use:     "rm <project> <service> <env> <host>",
	Aliases: []string{"remove", "delete"},
	Short:   "Remove one hostname from an environment's additionalHosts",
	Long: "Remove one hostname from an environment's additionalHosts.\n\n" +
		"The host stops serving as soon as the ingress reconciles — a\n" +
		"user-visible outage for anyone hitting that URL. Re-adding it later\n" +
		"re-requests a certificate, which counts against the Let's Encrypt\n" +
		"rate limit (50 certs/week per registered domain).",
	Example: `  kuso environment domain rm scubatony api staging api-staging.example.com`,
	Args:    cobra.ExactArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service, env, host := args[0], args[1], args[2], args[3]
		// Same guard + wording as the twin `domains remove` — the live
		// hostname goes dark either way.
		if err := confirmDestructive(envDomainRmYes,
			fmt.Sprintf("Unbind %s from %s/%s env %s? The hostname stops serving and re-adding it re-requests a certificate.",
				host, project, service, env)); err != nil {
			return err
		}
		resp, err := api.RemoveEnvDomain(project, service, env, host)
		if err != nil {
			return fmt.Errorf("remove env domain: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("removed %s from %s/%s env %s\n", host, project, service, env)
		return nil
	},
}

var environmentDomainSetCmd = &cobra.Command{
	Use:   "set <project> <service> <env> [host...]",
	Short: "Replace an environment's additionalHosts list (no hosts = clear all)",
	Long: `Replace the environment's entire additionalHosts list with the hosts given.
Pass no hosts to clear every additional host on that env.

Any host dropped from the list stops serving as soon as the ingress
reconciles, and re-adding one later re-requests its certificate
(Let's Encrypt rate limits apply).`,
	Example: `  kuso environment domain set scubatony api staging a.example.com b.example.com
  kuso environment domain set scubatony api staging   # clears all`,
	Args: cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service, env := args[0], args[1], args[2]
		hosts := args[3:]
		// This is a whole-list REPLACE: anything not in the new list goes
		// dark. Guard it like `domains remove`, with a sharper prompt for
		// the clear-all form.
		prompt := fmt.Sprintf("Replace additionalHosts on %s/%s env %s with %d host(s)? Hosts not in the new list stop serving.",
			project, service, env, len(hosts))
		if len(hosts) == 0 {
			prompt = fmt.Sprintf("Clear ALL additional hosts on %s/%s env %s? Every additional hostname stops serving.",
				project, service, env)
		}
		if err := confirmDestructive(envDomainSetYes, prompt); err != nil {
			return err
		}
		resp, err := api.SetEnvDomains(project, service, env, hosts)
		if err != nil {
			return fmt.Errorf("set env domains: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("set %d host(s) on %s/%s env %s\n", len(hosts), project, service, env)
		return nil
	},
}

func init() {
	// environmentCmd is the package-level var defined in environment.go.
	environmentCmd.AddCommand(environmentDomainCmd)
	environmentDomainAddCmd.Flags().StringVar(&envDomainAddTLSSecret, "tls-secret", "", "pre-provisioned TLS secret name (required for wildcard hosts)")
	environmentDomainCmd.AddCommand(environmentDomainAddCmd)
	environmentDomainRmCmd.Flags().BoolVarP(&envDomainRmYes, "yes", "y", false, "skip the confirmation prompt")
	environmentDomainCmd.AddCommand(environmentDomainRmCmd)
	environmentDomainSetCmd.Flags().BoolVarP(&envDomainSetYes, "yes", "y", false, "skip the confirmation prompt")
	environmentDomainCmd.AddCommand(environmentDomainSetCmd)
}
