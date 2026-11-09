package kusoCli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// domainStatus is one host of GET .../envs/{env}/domains/status.
type domainStatus struct {
	Host string `json:"host"`
	Kind string `json:"kind"` // auto | custom | wildcard
	DNS  struct {
		Status   string   `json:"status"` // ok | mismatch | unresolved | proxied | skipped
		Resolved []string `json:"resolved"`
		Message  string   `json:"message"`
	} `json:"dns"`
	TLS struct {
		Status   string `json:"status"` // ready | issuing | failed | missing | external | unknown
		NotAfter string `json:"notAfter"`
		Reason   string `json:"reason"`
		Message  string `json:"message"`
		Cause    string `json:"cause"`
		Since    string `json:"since"`
	} `json:"tls"`
}

// domainVerdict is the dashboard's one-line state for a domain and what
// to do about it. Mirrors summarizeDomain/diagnose in the web's
// domainHelpers.ts, keyed on the server's tls.cause.
func domainVerdict(d domainStatus) (string, string) {
	switch d.TLS.Cause {
	case "cert_manager_missing":
		return "HTTP only", "cert-manager isn't installed, so no certificate can be requested"
	case "rate_limited":
		return "Rate limited", "Let's Encrypt rate limit reached; kuso keeps retrying. Don't remove and re-add the domain"
	case "caa":
		return "Blocked by CAA", `add a CAA record 0 issue "letsencrypt.org" or remove the existing CAA records`
	}
	switch d.DNS.Status {
	case "mismatch":
		at := ""
		if len(d.DNS.Resolved) > 0 {
			at = " (" + strings.Join(d.DNS.Resolved, ", ") + ")"
		}
		return "Points elsewhere", "DNS resolves to another server" + at + "; point it at the cluster"
	case "unresolved":
		return "Waiting for DNS", "no DNS record yet; create it (see kuso domains list)"
	}
	switch d.TLS.Status {
	case "failed":
		detail := strings.Trim(d.TLS.Reason+": "+d.TLS.Message, ": ")
		switch d.TLS.Cause {
		case "cloudflare_proxy":
			detail = "Cloudflare's proxy may block validation: set the record to DNS only until the certificate is issued"
		case "expired":
			detail = "renewal didn't succeed before the certificate ran out; kuso keeps retrying"
		case "validation_failed":
			detail = "DNS points here, but Let's Encrypt's validation request didn't reach the service"
		}
		return "Certificate failed", detail
	case "issuing":
		return "Issuing certificate", "DNS is pointed; Let's Encrypt usually issues within a minute"
	case "missing":
		return "Waiting for certificate", "requested once the ingress picks the domain up"
	case "ready":
		if d.DNS.Status == "proxied" {
			return "Valid (behind Cloudflare)", ""
		}
		return "Valid configuration", ""
	case "external":
		return "Your certificate", "served with the TLS secret you provided"
	}
	return "Status unknown", d.TLS.Message
}

var (
	domainsStatusOutput string
	domainsStatusFresh  bool
)

var domainsStatusCmd = &cobra.Command{
	Use:   "status <project> <service>",
	Short: "Check each domain's DNS and TLS certificate state",
	Long: `Resolve every hostname an environment serves (auto domain, custom domains,
wildcards) and read its certificate, then say what's wrong and what to do:
DNS pointing elsewhere, a certificate still issuing, a Let's Encrypt rate
limit, a CAA record, Cloudflare's proxy in the way.

Results are cached for 30s server-side; --fresh re-checks now.`,
	Example: `  kuso domains status shop web
  kuso domains status shop web --env staging --fresh -o json`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := checkOutput(domainsStatusOutput); err != nil {
			return err
		}
		env := domainsEnv
		if env == "" {
			env = "production"
		}
		path := servicePath(args[0], args[1], "envs/"+url.PathEscape(env)+"/domains/status")
		if domainsStatusFresh {
			path += "?fresh=1"
		}
		resp, err := api.RawGet(path)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("domain status: %w", err)
		}
		var out struct {
			CheckedAt   string         `json:"checkedAt"`
			ExpectedIPs []string       `json:"expectedIPs"`
			Domains     []domainStatus `json:"domains"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if domainsStatusOutput == "json" {
			return jsonOut(out)
		}
		if len(out.Domains) == 0 {
			fmt.Printf("%s/%s [%s] serves no domains\n", args[0], args[1], env)
			return nil
		}
		t := tablewriter.NewWriter(os.Stdout)
		t.SetAutoWrapText(false)
		t.SetHeader([]string{"HOST", "KIND", "DNS", "TLS", "STATUS", "WHAT TO DO"})
		for _, d := range out.Domains {
			label, detail := domainVerdict(d)
			t.Append([]string{d.Host, d.Kind, d.DNS.Status, d.TLS.Status, label, truncate(detail, 90)})
		}
		t.Render()
		if len(out.ExpectedIPs) > 0 {
			fmt.Printf("domains should resolve to %s (checked %s ago)\n", strings.Join(out.ExpectedIPs, ", "), relativeAge(out.CheckedAt))
		}
		return nil
	},
}

func init() {
	domainsStatusCmd.Flags().StringVarP(&domainsStatusOutput, "output", "o", "table", "output format [table, json]")
	domainsStatusCmd.Flags().BoolVar(&domainsStatusFresh, "fresh", false, "bypass the 30s cache and re-check now")
	domainsCmd.AddCommand(domainsStatusCmd)
}
