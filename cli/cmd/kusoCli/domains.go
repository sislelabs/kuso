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

var domainsListCmd = &cobra.Command{
	Use:   "list <project> <service>",
	Short: "List custom domains on a service",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetService(args[0], args[1])
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
		req := kusoApi.AddDomainRequest{Host: host, TLS: !domainsAddNoTLS}
		resp, err := api.AddDomain(args[0], args[1], req)
		if err != nil {
			return fmt.Errorf("add domain: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("bound %s to %s/%s — point DNS A-record at the cluster IP if you haven't already\n", host, args[0], args[1])
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
		resp, err := api.RemoveDomain(args[0], args[1], args[2])
		if err != nil {
			return fmt.Errorf("remove domain: %w", err)
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("%s is not bound to %s/%s", args[2], args[0], args[1])
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("unbound %s from %s/%s\n", args[2], args[0], args[1])
		return nil
	},
}

func init() {
	domainsAddCmd.Flags().BoolVar(&domainsAddNoTLS, "no-tls", false, "skip the cert-manager TLS entry (HTTP-only host)")
	domainsCmd.AddCommand(domainsListCmd, domainsAddCmd, domainsRemoveCmd)
	rootCmd.AddCommand(domainsCmd)
}
