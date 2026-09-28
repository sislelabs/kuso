package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso drain` — ship app logs to an external sink (admin-only).
//
//   kuso drain list [-o json]
//   kuso drain add --type otlp|http|loki --url … [--header K=V]… [--project p] [--secret s]
//   kuso drain test <id>
//   kuso drain delete <id> [--yes]

var (
	drainType       string
	drainURL        string
	drainName       string
	drainProject    string
	drainSecret     string
	drainHeaders    []string
	drainDisabled   bool
	drainListOutput string
	drainDeleteYes  bool
)

var drainCmd = &cobra.Command{
	Use:     "drain",
	Aliases: []string{"drains"},
	Short:   "Forward app logs to an external sink (HTTP JSON, OTLP, Loki)",
	Long: `Log drains forward every app log line kuso collects to an external
sink, instance-wide or for one project. Types:

  http  POST a JSON array of {ts, project, service, env, pod, stream, line};
        --secret adds X-Kuso-Signature / X-Hub-Signature-256 HMAC headers
  otlp  OTLP/HTTP logs, JSON encoding (/v1/logs is appended to a base URL)
  loki  Loki push API (/loki/api/v1/push is appended to a base URL);
        labels project, service, env

Credentials in the URL (https://user:token@host) become an
Authorization: Basic header. Header values and the secret are masked on read.

For metrics, scrape GET /api/metrics/export (Prometheus text format) with
an admin API token or KUSO_METRICS_SCRAPE_TOKEN.`,
}

var drainListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List log drains",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListDrains()
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		var items []map[string]any
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode drains: %w", err)
		}
		if drainListOutput == "json" {
			return jsonOut(items)
		}
		if len(items) == 0 {
			fmt.Println("no drains configured")
			return nil
		}
		t := tablewriter.NewWriter(os.Stdout)
		t.SetHeader([]string{"ID", "NAME", "TYPE", "SCOPE", "ENABLED", "URL", "HEADERS"})
		for _, d := range items {
			scope := asString(d["project"])
			if scope == "" {
				scope = "(all projects)"
			}
			var hs []string
			if h, ok := d["headers"].(map[string]any); ok {
				for k := range h {
					hs = append(hs, k)
				}
			}
			sort.Strings(hs)
			t.Append([]string{asString(d["id"]), asString(d["name"]), asString(d["type"]), scope,
				fmt.Sprintf("%v", d["enabled"]), asString(d["url"]), strings.Join(hs, ",")})
		}
		t.Render()
		return nil
	},
}

// parseHeaderFlags turns repeated K=V flags into a map. Splits on the
// first '=' so values like "Bearer a=b" survive.
func parseHeaderFlags(in []string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for _, kv := range in {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("--header %q: want KEY=VALUE", kv)
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, nil
}

var drainAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a log drain",
	Args:  cobra.NoArgs,
	Example: `  kuso drain add --type otlp --url https://otlp-gateway-prod-eu-west-2.grafana.net/otlp \
    --header "Authorization=Basic $(printf '%s:%s' "$INSTANCE_ID" "$TOKEN" | base64)"
  kuso drain add --type loki --url https://USER:TOKEN@logs-prod-012.grafana.net --project shop
  kuso drain add --type http --url https://logs.example.com/ingest --secret "$HMAC_KEY"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if drainType == "" || drainURL == "" {
			return fmt.Errorf("--type and --url are required")
		}
		headers, err := parseHeaderFlags(drainHeaders)
		if err != nil {
			return err
		}
		enabled := !drainDisabled
		resp, err := api.CreateDrain(kusoApi.DrainBody{
			Name: drainName, Type: strings.ToLower(drainType), URL: drainURL, Project: drainProject,
			Headers: headers, Secret: drainSecret, Enabled: &enabled,
		})
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		var d map[string]any
		if err := json.Unmarshal(resp.Body(), &d); err != nil {
			return fmt.Errorf("decode drain: %w", err)
		}
		fmt.Printf("drain %q created (id=%s). Verify it with: kuso drain test %s\n",
			asString(d["name"]), asString(d["id"]), asString(d["id"]))
		return nil
	},
}

var drainTestCmd = &cobra.Command{
	Use:   "test <id>",
	Short: "Send one sample log line to a drain and show the upstream response",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.TestDrain(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("drain test failed: %w", err)
		}
		var r struct {
			Status int `json:"status"`
		}
		_ = json.Unmarshal(resp.Body(), &r)
		fmt.Printf("drain %s accepted the test line (upstream %d)\n", args[0], r.Status)
		return nil
	},
}

var drainDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"rm"},
	Short:   "Delete a log drain",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(drainDeleteYes,
			fmt.Sprintf("Delete drain %s? Logs stop forwarding within ~30s and its credentials are not recoverable.", args[0])); err != nil {
			return err
		}
		resp, err := api.DeleteDrain(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		fmt.Printf("drain %s deleted\n", args[0])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(drainCmd)
	drainCmd.AddCommand(drainListCmd, drainAddCmd, drainTestCmd, drainDeleteCmd)
	drainListCmd.Flags().StringVarP(&drainListOutput, "output", "o", "table", "output format [table, json]")
	f := drainAddCmd.Flags()
	f.StringVar(&drainType, "type", "", "drain type: otlp, http or loki")
	f.StringVar(&drainURL, "url", "", "sink URL (OTLP/Loki accept a base URL)")
	f.StringVar(&drainName, "name", "", "display name (defaults to type → host)")
	f.StringVar(&drainProject, "project", "", "only forward this project's logs (default: all projects)")
	f.StringVar(&drainSecret, "secret", "", "HMAC-SHA256 key; signs each batch body")
	f.StringArrayVar(&drainHeaders, "header", nil, "extra request header KEY=VALUE (repeatable)")
	f.BoolVar(&drainDisabled, "disabled", false, "create the drain paused")
	drainDeleteCmd.Flags().BoolVarP(&drainDeleteYes, "yes", "y", false, "skip the confirmation prompt")
}
