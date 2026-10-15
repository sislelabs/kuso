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
//   kuso drain get <id> [-o json]
//   kuso drain update <id> [--url …] [--name …] [--project p] [--header K=V]… [--remove-header K]… [--secret s | --clear-secret] [--enabled=bool]
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

var drainGetOutput string

var drainGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one log drain (header values and secret are masked)",
	Example: `  kuso drain get d1
  kuso drain get d1 -o json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetDrain(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		var d map[string]any
		if err := json.Unmarshal(resp.Body(), &d); err != nil {
			return fmt.Errorf("decode drain: %w", err)
		}
		switch drainGetOutput {
		case "json":
			return jsonOut(d)
		case "table", "":
		default:
			return fmt.Errorf("unsupported output format %q", drainGetOutput)
		}
		scope := asString(d["project"])
		if scope == "" {
			scope = "(all projects)"
		}
		fmt.Printf("id:       %s\n", asString(d["id"]))
		fmt.Printf("name:     %s\n", asString(d["name"]))
		fmt.Printf("type:     %s\n", asString(d["type"]))
		fmt.Printf("url:      %s\n", asString(d["url"]))
		fmt.Printf("scope:    %s\n", scope)
		fmt.Printf("enabled:  %v\n", d["enabled"])
		secret := "none"
		if asString(d["secret"]) != "" {
			secret = "set"
		}
		fmt.Printf("secret:   %s\n", secret)
		if h, ok := d["headers"].(map[string]any); ok && len(h) > 0 {
			keys := make([]string, 0, len(h))
			for k := range h {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Printf("headers:  %s (values masked)\n", strings.Join(keys, ", "))
		}
		return nil
	},
}

var drainUpdate struct {
	url, name, project, secret, typ string
	headers, removeHeaders          []string
	clearSecret, enabled            bool
}

// drainFromRead converts a masked GET into an update body. Masked header
// values and a masked secret are sent back as-is: the server reads the
// mask sentinel as "keep the stored value".
func drainFromRead(raw []byte) (kusoApi.DrainBody, error) {
	var cur struct {
		Name    string            `json:"name"`
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Project string            `json:"project"`
		Headers map[string]string `json:"headers"`
		Secret  string            `json:"secret"`
		Enabled bool              `json:"enabled"`
	}
	if err := json.Unmarshal(raw, &cur); err != nil {
		return kusoApi.DrainBody{}, fmt.Errorf("decode drain: %w", err)
	}
	enabled := cur.Enabled
	return kusoApi.DrainBody{
		Name: cur.Name, Type: cur.Type, URL: cur.URL, Project: cur.Project,
		Headers: cur.Headers, Secret: cur.Secret, Enabled: &enabled,
	}, nil
}

func applyDrainUpdateFlags(cmd *cobra.Command, body kusoApi.DrainBody) (kusoApi.DrainBody, error) {
	f := &drainUpdate
	ch := cmd.Flags().Changed
	if ch("secret") && f.clearSecret {
		return body, fmt.Errorf("pass --secret or --clear-secret, not both")
	}
	if ch("url") {
		body.URL = f.url
	}
	if ch("name") {
		body.Name = f.name
	}
	if ch("type") {
		body.Type = strings.ToLower(f.typ)
	}
	if ch("project") {
		body.Project = f.project
	}
	if ch("secret") {
		body.Secret = f.secret
	}
	if f.clearSecret {
		body.Secret = ""
	}
	if ch("enabled") {
		e := f.enabled
		body.Enabled = &e
	}
	add, err := parseHeaderFlags(f.headers)
	if err != nil {
		return body, err
	}
	if len(add) > 0 || len(f.removeHeaders) > 0 {
		h := make(map[string]string, len(body.Headers)+len(add))
		for k, v := range body.Headers {
			h[k] = v
		}
		for _, k := range f.removeHeaders {
			if _, ok := h[k]; !ok {
				return body, fmt.Errorf("--remove-header %q: drain has no such header", k)
			}
			delete(h, k)
		}
		for k, v := range add {
			h[k] = v
		}
		body.Headers = h
	}
	return body, nil
}

var drainUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Change a log drain's URL, name, scope, headers, secret or enabled state",
	Long: `Change a log drain. Only the flags you pass change; header values and
the HMAC secret you don't touch keep their stored values (they are masked
on read and never round-trip in clear). --header adds or replaces one
header, --remove-header drops one, --clear-secret stops signing batches.
--project '' widens the drain to every project.`,
	Example: `  kuso drain update d1 --url https://logs-prod-012.grafana.net
  kuso drain update d1 --header "Authorization=Bearer $NEW_TOKEN"
  kuso drain update d1 --enabled=false
  kuso drain update d1 --project '' --remove-header X-Scope-OrgID`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if cmd.Flags().NFlag() == 0 {
			return fmt.Errorf("nothing to change: pass at least one flag (see --help)")
		}
		resp, err := api.GetDrain(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		body, err := drainFromRead(resp.Body())
		if err != nil {
			return err
		}
		body, err = applyDrainUpdateFlags(cmd, body)
		if err != nil {
			return err
		}
		resp, err = api.UpdateDrain(args[0], body)
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		fmt.Printf("drain %s updated. Verify it with: kuso drain test %s\n", args[0], args[0])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(drainCmd)
	drainCmd.AddCommand(drainListCmd, drainAddCmd, drainTestCmd, drainDeleteCmd, drainGetCmd, drainUpdateCmd)
	drainGetCmd.Flags().StringVarP(&drainGetOutput, "output", "o", "table", "output format [table, json]")
	uf := drainUpdateCmd.Flags()
	uf.StringVar(&drainUpdate.url, "url", "", "new sink URL")
	uf.StringVar(&drainUpdate.name, "name", "", "new display name")
	uf.StringVar(&drainUpdate.typ, "type", "", "change the drain type: otlp, http or loki")
	uf.StringVar(&drainUpdate.project, "project", "", "only forward this project's logs ('' = all projects)")
	uf.StringVar(&drainUpdate.secret, "secret", "", "new HMAC-SHA256 signing key")
	uf.BoolVar(&drainUpdate.clearSecret, "clear-secret", false, "remove the signing key")
	uf.StringArrayVar(&drainUpdate.headers, "header", nil, "add or replace a request header KEY=VALUE (repeatable)")
	uf.StringArrayVar(&drainUpdate.removeHeaders, "remove-header", nil, "drop a request header by name (repeatable)")
	uf.BoolVar(&drainUpdate.enabled, "enabled", true, "--enabled=false pauses the drain, --enabled resumes it")
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
