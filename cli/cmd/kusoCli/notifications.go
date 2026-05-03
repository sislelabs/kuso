package kusoCli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso notifications` — CRUD for Discord / webhook / Slack notification
// channels. Feature-parity with the web UI at /settings/notifications.
//
//   kuso notifications list [-o json]
//   kuso notifications get <id> [-o json]
//   kuso notifications create discord --name disco --url https://… \
//       --events build.failed,pod.crashed --mention here --mention-on build.failed
//   kuso notifications create webhook --name ci-hook --url https://… \
//       --secret abc123 --events build.succeeded
//   kuso notifications update <id> [...same flags as create...]
//   kuso notifications delete <id>
//   kuso notifications test   <id>
//   kuso notifications enable <id>
//   kuso notifications disable <id>

var notificationsCmd = &cobra.Command{
	Use:     "notifications",
	Aliases: []string{"notification", "notify", "notif"},
	Short:   "Manage notification channels (Discord / webhook / Slack)",
}

var notificationsListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List notification channels",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListNotifications()
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		items, err := unwrapNotifications(resp.Body())
		if err != nil {
			return err
		}
		switch outputFormat {
		case "json":
			return jsonOut(items)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "NAME", "TYPE", "ENABLED", "EVENTS"})
			for _, n := range items {
				ev, _ := n["events"].([]any)
				evs := make([]string, 0, len(ev))
				for _, e := range ev {
					if s, ok := e.(string); ok {
						evs = append(evs, s)
					}
				}
				t.Append([]string{
					asString(n["id"]),
					asString(n["name"]),
					asString(n["type"]),
					fmt.Sprintf("%v", n["enabled"]),
					strings.Join(evs, ","),
				})
			}
			t.Render()
			return nil
		}
	},
}

var notificationsGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one notification channel (full config; --output json for machine-readable)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetNotification(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		one, err := unwrapNotification(resp.Body())
		if err != nil {
			return err
		}
		if outputFormat == "json" {
			return jsonOut(one)
		}
		// Pretty print: redact webhook URLs by default to avoid
		// shoulder-surfing the secret token. Pass --reveal to show.
		if !notifReveal {
			if cfg, ok := one["config"].(map[string]any); ok {
				if u, _ := cfg["url"].(string); u != "" {
					cfg["url"] = redactURL(u)
				}
				if s, _ := cfg["secret"].(string); s != "" {
					cfg["secret"] = "***"
				}
			}
		}
		buf, _ := json.MarshalIndent(one, "", "  ")
		fmt.Println(string(buf))
		return nil
	},
}

// flag state shared by create + update.
var (
	notifName       string
	notifURL        string
	notifSecret     string
	notifChannel    string // slack only
	notifEvents     []string
	notifPipelines  []string
	notifEnabled    bool
	notifMention    string   // global mention: "", "here", "everyone", "<role-id>"
	notifMentionOn  []string // events that trigger the mention; empty = always
	notifReveal     bool     // get: show webhook URL/secret in plaintext
	notifEditConfig string   // update: replace config from JSON file or '-' for stdin
)

var notificationsCreateCmd = &cobra.Command{
	Use:   "create <type>",
	Short: "Create a notification channel (type: discord, webhook, slack)",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso notifications create discord --name disco --url 'https://discord.com/api/webhooks/…' \
    --events build.started,build.succeeded,build.failed,pod.crashed \
    --mention here --mention-on build.failed,pod.crashed
  kuso notifications create webhook --name ci-hook --url 'https://ci.example/hook' --secret topsecret`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		typ := strings.ToLower(args[0])
		body, err := buildNotifBody(typ)
		if err != nil {
			return err
		}
		resp, err := api.CreateNotification(body)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		one, err := unwrapNotification(resp.Body())
		if err != nil {
			return err
		}
		fmt.Printf("notification %q created (id=%s)\n", asString(one["name"]), asString(one["id"]))
		return nil
	},
}

var notificationsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an existing notification channel (only the flags you pass are changed)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// Pull existing so unspecified flags retain their value.
		resp, err := api.GetNotification(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		existing, err := unwrapNotification(resp.Body())
		if err != nil {
			return err
		}
		body := kusoApi.NotificationBody{
			Name:    asString(existing["name"]),
			Enabled: asBool(existing["enabled"]),
			Type:    asString(existing["type"]),
		}
		if pp, ok := existing["pipelines"].([]any); ok {
			for _, x := range pp {
				if s, ok := x.(string); ok {
					body.Pipelines = append(body.Pipelines, s)
				}
			}
		}
		if ee, ok := existing["events"].([]any); ok {
			for _, x := range ee {
				if s, ok := x.(string); ok {
					body.Events = append(body.Events, s)
				}
			}
		}
		if cfg, ok := existing["config"].(map[string]any); ok {
			body.Config = cfg
		} else {
			body.Config = map[string]any{}
		}
		// Apply flag overrides.
		if cmd.Flags().Changed("name") {
			body.Name = notifName
		}
		if cmd.Flags().Changed("enabled") {
			body.Enabled = notifEnabled
		}
		if cmd.Flags().Changed("events") {
			body.Events = notifEvents
		}
		if cmd.Flags().Changed("pipelines") {
			body.Pipelines = notifPipelines
		}
		if cmd.Flags().Changed("url") {
			body.Config["url"] = notifURL
		}
		if cmd.Flags().Changed("secret") {
			body.Config["secret"] = notifSecret
		}
		if cmd.Flags().Changed("channel") {
			body.Config["channel"] = notifChannel
		}
		if cmd.Flags().Changed("mention") {
			body.Config["mention"] = notifMention
		}
		if cmd.Flags().Changed("mention-on") {
			body.Config["mentionOn"] = stringsToAny(notifMentionOn)
		}
		if notifEditConfig != "" {
			cfg, err := loadConfigFile(notifEditConfig)
			if err != nil {
				return err
			}
			body.Config = cfg
		}
		resp2, err := api.UpdateNotification(args[0], body)
		if err != nil {
			return err
		}
		if resp2.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp2.StatusCode(), string(resp2.Body()))
		}
		fmt.Printf("notification %s updated\n", args[0])
		return nil
	},
}

var notificationsDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"rm"},
	Short:   "Delete a notification channel",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DeleteNotification(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("notification %s deleted\n", args[0])
		return nil
	},
}

var notificationsTestCmd = &cobra.Command{
	Use:   "test <id>",
	Short: "Send a synthetic test event so you can verify the channel works",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.TestNotification(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			// Server now returns useful error text (502 with the
			// upstream Discord/webhook body) — surface it.
			return fmt.Errorf("test failed (%d): %s", resp.StatusCode(), strings.TrimSpace(string(resp.Body())))
		}
		fmt.Println("test sent.")
		return nil
	},
}

var notificationsEnableCmd = &cobra.Command{
	Use:   "enable <id>",
	Short: "Enable a notification channel",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setEnabled(args[0], true) },
}

var notificationsDisableCmd = &cobra.Command{
	Use:   "disable <id>",
	Short: "Disable a notification channel without deleting it",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setEnabled(args[0], false) },
}

// helpers ----------------------------------------------------------------

func setEnabled(id string, on bool) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	resp, err := api.GetNotification(id)
	if err != nil {
		return err
	}
	if resp.StatusCode() >= 300 {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
	}
	one, err := unwrapNotification(resp.Body())
	if err != nil {
		return err
	}
	body := kusoApi.NotificationBody{
		Name:    asString(one["name"]),
		Enabled: on,
		Type:    asString(one["type"]),
	}
	if cfg, ok := one["config"].(map[string]any); ok {
		body.Config = cfg
	}
	if ee, ok := one["events"].([]any); ok {
		for _, x := range ee {
			if s, ok := x.(string); ok {
				body.Events = append(body.Events, s)
			}
		}
	}
	if pp, ok := one["pipelines"].([]any); ok {
		for _, x := range pp {
			if s, ok := x.(string); ok {
				body.Pipelines = append(body.Pipelines, s)
			}
		}
	}
	resp2, err := api.UpdateNotification(id, body)
	if err != nil {
		return err
	}
	if resp2.StatusCode() >= 300 {
		return fmt.Errorf("server returned %d: %s", resp2.StatusCode(), string(resp2.Body()))
	}
	state := "enabled"
	if !on {
		state = "disabled"
	}
	fmt.Printf("notification %s %s\n", id, state)
	return nil
}

func buildNotifBody(typ string) (kusoApi.NotificationBody, error) {
	body := kusoApi.NotificationBody{
		Name:      notifName,
		Enabled:   notifEnabled,
		Type:      typ,
		Events:    notifEvents,
		Pipelines: notifPipelines,
		Config:    map[string]any{},
	}
	switch typ {
	case "discord", "webhook":
		if notifURL == "" {
			return body, fmt.Errorf("--url is required for %s notifications", typ)
		}
		body.Config["url"] = notifURL
		if notifSecret != "" {
			body.Config["secret"] = notifSecret
		}
	case "slack":
		if notifURL == "" || notifChannel == "" {
			return body, fmt.Errorf("--url and --channel are required for slack notifications")
		}
		body.Config["url"] = notifURL
		body.Config["channel"] = notifChannel
	default:
		return body, fmt.Errorf("unknown type %q; expected discord, webhook, or slack", typ)
	}
	if notifMention != "" {
		body.Config["mention"] = notifMention
	}
	if len(notifMentionOn) > 0 {
		body.Config["mentionOn"] = stringsToAny(notifMentionOn)
	}
	if notifEditConfig != "" {
		cfg, err := loadConfigFile(notifEditConfig)
		if err != nil {
			return body, err
		}
		body.Config = cfg
	}
	if body.Name == "" {
		return body, fmt.Errorf("--name is required")
	}
	return body, nil
}

func unwrapNotifications(buf []byte) ([]map[string]any, error) {
	var env struct {
		Success bool             `json:"success"`
		Data    []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(buf, &env); err != nil {
		return nil, fmt.Errorf("decode list: %w", err)
	}
	return env.Data, nil
}

func unwrapNotification(buf []byte) (map[string]any, error) {
	var env struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(buf, &env); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return env.Data, nil
}

func loadConfigFile(path string) (map[string]any, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = readAllStdin()
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	return out, nil
}

func readAllStdin() ([]byte, error) { return io.ReadAll(os.Stdin) }

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func stringsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// redactURL hides the trailing path segment (Discord webhook tokens
// always sit at the end of the URL).
func redactURL(u string) string {
	for i := len(u) - 1; i >= 0; i-- {
		if u[i] == '/' {
			return u[:i+1] + "***"
		}
	}
	return "***"
}

func init() {
	rootCmd.AddCommand(notificationsCmd)
	notificationsCmd.AddCommand(notificationsListCmd)
	notificationsListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")

	notificationsCmd.AddCommand(notificationsGetCmd)
	notificationsGetCmd.Flags().StringVarP(&outputFormat, "output", "o", "pretty", "output format [pretty, json]")
	notificationsGetCmd.Flags().BoolVar(&notifReveal, "reveal", false, "show webhook URL and secret instead of redacting")

	for _, c := range []*cobra.Command{notificationsCreateCmd, notificationsUpdateCmd} {
		c.Flags().StringVar(&notifName, "name", "", "human-readable name")
		c.Flags().StringVar(&notifURL, "url", "", "webhook URL (discord / webhook / slack)")
		c.Flags().StringVar(&notifSecret, "secret", "", "shared secret for HMAC (webhook only)")
		c.Flags().StringVar(&notifChannel, "channel", "", "slack channel (slack only)")
		c.Flags().StringSliceVar(&notifEvents, "events", nil, "comma-separated event whitelist (e.g. build.failed,pod.crashed). empty = all events")
		c.Flags().StringSliceVar(&notifPipelines, "pipelines", nil, "comma-separated project whitelist; empty = all projects")
		c.Flags().BoolVar(&notifEnabled, "enabled", true, "enable the channel")
		c.Flags().StringVar(&notifMention, "mention", "", "mention target: 'here', 'everyone', or '<role-id>'")
		c.Flags().StringSliceVar(&notifMentionOn, "mention-on", nil, "events that trigger the mention; empty = always")
		c.Flags().StringVar(&notifEditConfig, "config-file", "", "load config from JSON file (or - for stdin) — overrides --url/--secret/etc")
	}
	notificationsCmd.AddCommand(notificationsCreateCmd)
	notificationsCmd.AddCommand(notificationsUpdateCmd)
	notificationsCmd.AddCommand(notificationsDeleteCmd)
	notificationsCmd.AddCommand(notificationsTestCmd)
	notificationsCmd.AddCommand(notificationsEnableCmd)
	notificationsCmd.AddCommand(notificationsDisableCmd)
}
