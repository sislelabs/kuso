package kusoCli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso domains` is a discoverable shortcut over the same surface as
// `kuso project service set --domains ...`. The setting-domains flow
// is the most common UI footgun (rolling-restart, cert minting, DNS
// drift) so the CLI gets first-class verbs that explain what is
// actually happening.

var domainsCmd = &cobra.Command{
	Use:   "domains",
	Short: "Manage custom hostnames on a service",
	Long: `Add, remove, or list the custom hostnames bound to a service.

Each kuso service comes with an auto-domain (project.baseDomain or the
cluster default). Custom hostnames live alongside it: traefik mounts
them in the same Ingress and cert-manager mints a Let's Encrypt cert
per host once the DNS A-record points at the cluster IP.

Use 'kuso project update <project> --domain <baseDomain>' to change
the auto-domain for every service in the project at once.`,
}

var (
	domainsListOutput string
	// domainsEnv scopes the domain verbs to a single environment. Empty =
	// service-level: add/remove operate on spec.domains and the server
	// mirrors them to the PRODUCTION env. Set (e.g. "staging",
	// "preview-pr-7") = per-env: operate directly on that env's
	// additionalHosts, so staging can serve its own hostname without
	// production claiming it.
	domainsEnv string
)

// envCRName mirrors the server's envCRNameFor: <project>-<short-svc>-<env>.
func envCRName(project, service, env string) string {
	short := strings.TrimPrefix(service, project+"-")
	return project + "-" + short + "-" + env
}

var domainsListCmd = &cobra.Command{
	Use:   "list <project> <service>",
	Short: "List custom domains on a service (or one env with --env)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service := args[0], args[1]

		// --env: read the env CR's host + additionalHosts directly (no
		// service-level spec.domains involved).
		if domainsEnv != "" {
			resp, err := api.GetEnvironment(project, envCRName(project, service, domainsEnv))
			if err != nil {
				return fmt.Errorf("get environment: %w", err)
			}
			if resp.StatusCode() >= 300 {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
			}
			var env struct {
				Spec struct {
					Host            string   `json:"host"`
					AdditionalHosts []string `json:"additionalHosts"`
				} `json:"spec"`
			}
			if err := json.Unmarshal(resp.Body(), &env); err != nil {
				return fmt.Errorf("decode environment: %w", err)
			}
			if domainsListOutput == "json" {
				b, _ := json.MarshalIndent(env.Spec.AdditionalHosts, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			fmt.Printf("%s\t(auto-domain)\n", env.Spec.Host)
			for _, h := range env.Spec.AdditionalHosts {
				fmt.Printf("%s\t(custom, env=%s)\n", h, domainsEnv)
			}
			if len(env.Spec.AdditionalHosts) == 0 {
				fmt.Printf("no custom domains on %s/%s env=%s\n", project, service, domainsEnv)
			}
			return nil
		}

		resp, err := api.GetService(project, service)
		if err != nil {
			return fmt.Errorf("get service: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var svc struct {
			Spec struct {
				Domains []struct {
					Host string `json:"host"`
					TLS  bool   `json:"tls"`
				} `json:"domains"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(resp.Body(), &svc); err != nil {
			return fmt.Errorf("decode service: %w", err)
		}
		// JSON output: round-trip the parsed shape so scripts get a
		// stable schema (no auto-domain — that comes from project
		// settings, list separately if needed).
		if domainsListOutput == "json" {
			b, err := json.MarshalIndent(svc.Spec.Domains, "", "  ")
			if err != nil {
				return fmt.Errorf("encode domains: %w", err)
			}
			fmt.Println(string(b))
			return nil
		}
		if len(svc.Spec.Domains) == 0 {
			fmt.Printf("no custom domains on %s/%s — only the auto-domain is bound\n", args[0], args[1])
			return nil
		}
		for _, d := range svc.Spec.Domains {
			tls := "tls"
			if !d.TLS {
				tls = "no-tls"
			}
			fmt.Printf("%s\t%s\n", d.Host, tls)
		}
		return nil
	},
}

var (
	domainsAddNoTLS bool
)

var domainsAddCmd = &cobra.Command{
	Use:   "add <project> <service> <host>",
	Short: "Bind a custom hostname to a service",
	Long: `Append a custom hostname to a service. DNS must already point at
the cluster IP — kuso doesn't manage your registrar.

cert-manager will mint a Let's Encrypt cert on first request to the
host (HTTP-01 challenge). If the host doesn't resolve to the cluster
yet you'll see traefik default-cert during the propagation window.

Adds are idempotent: duplicate (host, tls) returns 409 with no change.`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		host := strings.TrimSpace(args[2])
		if host == "" {
			return fmt.Errorf("host must be non-empty")
		}
		project, service := args[0], args[1]

		// --env: bind directly to that environment's additionalHosts.
		if domainsEnv != "" {
			resp, err := api.AddEnvDomain(project, service, domainsEnv, host)
			if err != nil {
				return fmt.Errorf("add env domain: %w", err)
			}
			if resp.StatusCode() >= 300 {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
			}
			fmt.Printf("bound %s to %s/%s env=%s — point DNS A-record at the cluster IP if you haven't already\n", host, project, service, domainsEnv)
			return nil
		}

		req := kusoApi.AddDomainRequest{Host: host, TLS: !domainsAddNoTLS}
		resp, err := api.AddDomain(project, service, req)
		if err != nil {
			return fmt.Errorf("add domain: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("bound %s to %s/%s — point DNS A-record at the cluster IP if you haven't already\n", host, project, service)
		return nil
	},
}

var domainsRemoveCmd = &cobra.Command{
	Use:     "remove <project> <service> <host>",
	Aliases: []string{"rm"},
	Short:   "Unbind a custom hostname from a service",
	Args:    cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service, host := args[0], args[1], args[2]

		if domainsEnv != "" {
			resp, err := api.RemoveEnvDomain(project, service, domainsEnv, host)
			if err != nil {
				return fmt.Errorf("remove env domain: %w", err)
			}
			if resp.StatusCode() >= 300 {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
			}
			fmt.Printf("unbound %s from %s/%s env=%s\n", host, project, service, domainsEnv)
			return nil
		}

		resp, err := api.RemoveDomain(project, service, host)
		if err != nil {
			return fmt.Errorf("remove domain: %w", err)
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("%s is not bound to %s/%s", host, project, service)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("unbound %s from %s/%s\n", host, project, service)
		return nil
	},
}

func init() {
	domainsAddCmd.Flags().BoolVar(&domainsAddNoTLS, "no-tls", false, "skip the cert-manager TLS entry (HTTP-only host)")
	// --env on the group: scope add/remove/list to one environment. Empty
	// (default) = service-level (the server mirrors to production).
	domainsCmd.PersistentFlags().StringVar(&domainsEnv, "env", "",
		"scope to one environment (e.g. staging, preview-pr-7); empty = service-level + production mirror")
	domainsCmd.AddCommand(domainsListCmd, domainsAddCmd, domainsRemoveCmd)
	rootCmd.AddCommand(domainsCmd)
}
