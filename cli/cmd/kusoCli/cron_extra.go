// cron_extra.go: `kuso cron get` plus the deadline / failure-webhook flag
// helpers shared by the cron add and edit commands in cron.go.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

func checkCronDeadlines(timeout, startingDeadline int) error {
	if timeout < 0 {
		return fmt.Errorf("--timeout must be >= 0 seconds (got %d)", timeout)
	}
	if startingDeadline < 0 {
		return fmt.Errorf("--starting-deadline must be >= 0 seconds (got %d)", startingDeadline)
	}
	return nil
}

// parseCronSecretRef reads "name:key".
func parseCronSecretRef(v string) (*kusoApi.CronSecretKeyRef, error) {
	name, key, ok := strings.Cut(strings.TrimSpace(v), ":")
	if !ok || name == "" || key == "" {
		return nil, fmt.Errorf("--on-failure-secret must be secret-name:key (got %q)", v)
	}
	return &kusoApi.CronSecretKeyRef{Name: name, Key: key}, nil
}

// applyProjectCronCreateFlags sets the deadline and failure-webhook fields
// shared by `cron add-http` and `cron add-command`.
func applyProjectCronCreateFlags(cmd *cobra.Command, req *kusoApi.CreateProjectCronRequest) error {
	if err := checkCronDeadlines(pCronTimeout, pCronStartingDeadline); err != nil {
		return err
	}
	req.ActiveDeadlineSeconds = pCronTimeout
	req.StartingDeadlineSeconds = pCronStartingDeadline
	if cmd.Flags().Changed("pull-policy") && req.Kind != "command" {
		return fmt.Errorf("--pull-policy only applies to add-command crons")
	}
	if cmd.Flags().Changed("on-failure-secret") && pCronOnFailureWebhook == "" {
		return fmt.Errorf("--on-failure-secret needs --on-failure-webhook")
	}
	if pCronOnFailureWebhook != "" {
		of := &kusoApi.CronOnFailure{WebhookURL: pCronOnFailureWebhook}
		if cmd.Flags().Changed("on-failure-secret") {
			ref, err := parseCronSecretRef(pCronOnFailureSecret)
			if err != nil {
				return err
			}
			of.SecretRef = ref
		}
		req.OnFailure = of
	}
	return nil
}

// cronOnFailureUpdate builds the `cron edit` failure-webhook patch; nil
// when no failure flag was passed. The server replaces URL and secret
// together and ignores a block with no URL, so --on-failure-secret alone
// is refused rather than silently dropped.
func cronOnFailureUpdate(cmd *cobra.Command) (*kusoApi.CronOnFailureUpdate, error) {
	changed := cmd.Flags().Changed
	if pCronOnFailureClear {
		if changed("on-failure-webhook") || changed("on-failure-secret") {
			return nil, fmt.Errorf("--on-failure-clear can't be combined with --on-failure-webhook / --on-failure-secret")
		}
		return &kusoApi.CronOnFailureUpdate{Clear: true}, nil
	}
	if !changed("on-failure-webhook") && !changed("on-failure-secret") {
		return nil, nil
	}
	if strings.TrimSpace(pCronOnFailureWebhook) == "" {
		return nil, fmt.Errorf("--on-failure-webhook is required when editing the failure webhook (use --on-failure-clear to remove it)")
	}
	of := &kusoApi.CronOnFailureUpdate{WebhookURL: strings.TrimSpace(pCronOnFailureWebhook)}
	if changed("on-failure-secret") {
		ref, err := parseCronSecretRef(pCronOnFailureSecret)
		if err != nil {
			return nil, err
		}
		of.SecretRef = ref
	}
	return of, nil
}

var cronGetOutput string

var cronGetCmd = &cobra.Command{
	Use:   "get <project> <service> <name>",
	Short: "Show one service-attached cron",
	Example: `  kuso cron get myproj api daily-cleanup
  kuso cron get myproj api daily-cleanup -o json`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetCron(args[0], args[1], args[2])
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		var c map[string]any
		if err := json.Unmarshal(resp.Body(), &c); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if cronGetOutput == "json" {
			return jsonOut(c)
		}
		spec := mapAt(c, "spec")
		fmt.Printf("cron: %s\n", asString(mapAt(c, "metadata")["name"]))
		row := func(label string, v any) {
			if s := cronField(v); s != "" {
				fmt.Printf("  %-18s %s\n", label+":", s)
			}
		}
		row("display name", spec["displayName"])
		row("service", spec["service"])
		row("schedule", spec["schedule"])
		row("command", spec["command"])
		row("suspend", spec["suspend"])
		row("pin image", spec["pinImage"])
		row("concurrency", spec["concurrencyPolicy"])
		row("timeout (s)", spec["activeDeadlineSeconds"])
		row("starting deadline", spec["startingDeadlineSeconds"])
		img := mapAt(spec, "image")
		if repo := asString(img["repository"]); repo != "" {
			row("image", repo+":"+asString(img["tag"]))
		}
		if of := mapAt(spec, "onFailure"); len(of) > 0 {
			row("on failure", of["webhookURL"])
		}
		return nil
	},
}

// cronField renders a spec value for the table view; "" hides the row.
func cronField(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if !t {
			return ""
		}
		return "true"
	case float64:
		if t == 0 {
			return ""
		}
		return fmt.Sprintf("%d", int64(t))
	case []any:
		parts := make([]string, 0, len(t))
		for _, p := range t {
			parts = append(parts, fmt.Sprint(p))
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprint(t)
	}
}

func init() {
	cronGetCmd.Flags().StringVarP(&cronGetOutput, "output", "o", "table", "output format [table, json]")
	cronCmd.AddCommand(cronGetCmd)
}
