// incident_agent_settings.go — the admin incident-agent settings page
// and incident feedback, from the CLI:
//
//	kuso incident-agent settings get [-o json]
//	kuso incident-agent settings set [--enabled] [--trigger-*] [--max-concurrent N] [--cooldown-hours N] [--agent-image ref] [--dry-run]
//	kuso incident-agent set-discord [--bot-token-file f|-] [--kuso-bot-token-file f|-] [--channel id | --clear-channel] [--dry-run]
//	kuso incident feedback <id> --text "…" | --file f|-

package kusoCli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// readValueFrom reads a file, or stdin for "-", dropping one trailing
// newline. Used for secrets and long text that shouldn't sit in argv.
func readValueFrom(path string) (string, error) {
	var b []byte
	var err error
	if path == "-" {
		b, err = io.ReadAll(secretStdin)
	} else {
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return "", err
	}
	v := strings.TrimSuffix(string(b), "\n")
	return strings.TrimSuffix(v, "\r"), nil
}

type incidentAgentSettings struct {
	Config kusoApi.IncidentAgentConfig `json:"config"`
	Status struct {
		CCConfigured       bool   `json:"ccConfigured"`
		CCExpiresAt        string `json:"ccExpiresAt,omitempty"`
		CCSubscriptionType string `json:"ccSubscriptionType,omitempty"`
		DiscordConfigured  bool   `json:"discordConfigured"`
		ChannelID          string `json:"channelId,omitempty"`
		BotDeployed        bool   `json:"botDeployed"`
		BotReady           bool   `json:"botReady"`
		OpenIncidents      int    `json:"openIncidents"`
	} `json:"status"`
}

func fetchIncidentAgentSettings() (*incidentAgentSettings, error) {
	resp, err := api.GetIncidentAgentSettings()
	if err := checkRespErr(resp, err); err != nil {
		return nil, fmt.Errorf("get incident-agent settings: %w", err)
	}
	var s incidentAgentSettings
	if err := json.Unmarshal(resp.Body(), &s); err != nil {
		return nil, fmt.Errorf("decode settings: %w", err)
	}
	return &s, nil
}

func printIncidentAgentConfig(c kusoApi.IncidentAgentConfig) {
	fmt.Printf("enabled:         %v\n", c.Enabled)
	fmt.Printf("triggers:        pod=%v alert=%v node=%v\n", c.TriggerPod, c.TriggerAlert, c.TriggerNode)
	fmt.Printf("max concurrent:  %d\n", c.MaxConcurrent)
	fmt.Printf("cooldown hours:  %d\n", c.CooldownHours)
	fmt.Printf("agent image:     %s\n", orNone(c.AgentImage))
}

var incidentAgentSettingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Show or change the incident agent's configuration (admin)",
}

var incidentAgentSettingsGetOutput string

var incidentAgentSettingsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the incident agent's config and wiring status",
	Long: `Show the incident agent's config (enabled, triggers, concurrency,
cooldown, image override) and its wiring: whether Claude Code credentials
and the Discord bot are configured, and how many incidents are open.
Secret values are never returned.`,
	Example: `  kuso incident-agent settings get
  kuso incident-agent settings get -o json | jq .status`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		s, err := fetchIncidentAgentSettings()
		if err != nil {
			return err
		}
		switch incidentAgentSettingsGetOutput {
		case "json":
			return jsonOut(s)
		case "table", "":
		default:
			return fmt.Errorf("unsupported output format %q", incidentAgentSettingsGetOutput)
		}
		printIncidentAgentConfig(s.Config)
		cc := "not configured"
		if s.Status.CCConfigured {
			cc = "configured"
			if s.Status.CCSubscriptionType != "" {
				cc += " (" + s.Status.CCSubscriptionType + ")"
			}
			if s.Status.CCExpiresAt != "" {
				cc += ", expires " + s.Status.CCExpiresAt
			}
		}
		fmt.Printf("claude code:     %s\n", cc)
		discord := "not configured"
		if s.Status.DiscordConfigured {
			discord = "configured"
		}
		if s.Status.ChannelID != "" {
			discord += ", channel " + s.Status.ChannelID
		}
		fmt.Printf("discord:         %s\n", discord)
		fmt.Printf("bot:             deployed=%v ready=%v\n", s.Status.BotDeployed, s.Status.BotReady)
		fmt.Printf("open incidents:  %d\n", s.Status.OpenIncidents)
		return nil
	},
}

var incidentAgentSettingsSet struct {
	enabled, triggerPod, triggerAlert, triggerNode bool
	maxConcurrent, cooldownHours                   int
	agentImage                                     string
	dryRun                                         bool
}

// applyIncidentAgentFlags overlays only the flags the user passed onto
// the current config. The PUT is a full replace, so anything not
// overlaid must keep its current value.
func applyIncidentAgentFlags(cmd *cobra.Command, cur kusoApi.IncidentAgentConfig) (kusoApi.IncidentAgentConfig, bool) {
	f := &incidentAgentSettingsSet
	changed := false
	set := func(name string, apply func()) {
		if cmd.Flags().Changed(name) {
			apply()
			changed = true
		}
	}
	set("enabled", func() { cur.Enabled = f.enabled })
	set("trigger-pod", func() { cur.TriggerPod = f.triggerPod })
	set("trigger-alert", func() { cur.TriggerAlert = f.triggerAlert })
	set("trigger-node", func() { cur.TriggerNode = f.triggerNode })
	set("max-concurrent", func() { cur.MaxConcurrent = f.maxConcurrent })
	set("cooldown-hours", func() { cur.CooldownHours = f.cooldownHours })
	set("agent-image", func() { cur.AgentImage = f.agentImage })
	return cur, changed
}

var incidentAgentSettingsSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Change incident-agent config knobs (applies within seconds)",
	Long: `Change the incident agent's config. Only the flags you pass change;
the rest keep their current values. Boolean flags take an explicit value
to turn something off (--enabled=false). --max-concurrent 0 or
--cooldown-hours 0 disables that limit. --dry-run prints the config that
would be saved without saving it.`,
	Example: `  kuso incident-agent settings set --enabled
  kuso incident-agent settings set --enabled=false
  kuso incident-agent settings set --trigger-node=false --max-concurrent 2 --cooldown-hours 6
  kuso incident-agent settings set --agent-image ghcr.io/acme/agent:v2 --dry-run`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		s, err := fetchIncidentAgentSettings()
		if err != nil {
			return err
		}
		next, changed := applyIncidentAgentFlags(cmd, s.Config)
		if !changed {
			return fmt.Errorf("nothing to change: pass at least one of --enabled, --trigger-pod, --trigger-alert, --trigger-node, --max-concurrent, --cooldown-hours, --agent-image")
		}
		if next.MaxConcurrent < 0 || next.MaxConcurrent > 50 {
			return fmt.Errorf("--max-concurrent must be 0..50")
		}
		if next.CooldownHours < 0 || next.CooldownHours > 168 {
			return fmt.Errorf("--cooldown-hours must be 0..168")
		}
		if incidentAgentSettingsSet.dryRun {
			fmt.Println("dry run: would save")
			printIncidentAgentConfig(next)
			return nil
		}
		resp, err := api.PutIncidentAgentSettings(next)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("save incident-agent settings: %w", err)
		}
		fmt.Println("incident-agent settings saved")
		printIncidentAgentConfig(next)
		return nil
	},
}

var incidentAgentDiscord struct {
	botTokenFile, kusoBotTokenFile, channel string
	clearChannel, dryRun                    bool
}

func buildIncidentAgentDiscord(cmd *cobra.Command) (kusoApi.IncidentAgentDiscord, error) {
	f := &incidentAgentDiscord
	var body kusoApi.IncidentAgentDiscord
	if f.botTokenFile == "-" && f.kusoBotTokenFile == "-" {
		return body, fmt.Errorf("only one of --bot-token-file and --kuso-bot-token-file can read stdin")
	}
	if cmd.Flags().Changed("channel") && f.clearChannel {
		return body, fmt.Errorf("pass --channel or --clear-channel, not both")
	}
	if f.botTokenFile != "" {
		v, err := readValueFrom(f.botTokenFile)
		if err != nil {
			return body, fmt.Errorf("read --bot-token-file: %w", err)
		}
		if strings.TrimSpace(v) == "" {
			return body, fmt.Errorf("--bot-token-file is empty")
		}
		body.BotToken = strings.TrimSpace(v)
	}
	if f.kusoBotTokenFile != "" {
		v, err := readValueFrom(f.kusoBotTokenFile)
		if err != nil {
			return body, fmt.Errorf("read --kuso-bot-token-file: %w", err)
		}
		if strings.TrimSpace(v) == "" {
			return body, fmt.Errorf("--kuso-bot-token-file is empty")
		}
		body.KusoBotToken = strings.TrimSpace(v)
	}
	if cmd.Flags().Changed("channel") {
		ch := f.channel
		body.ChannelID = &ch
	} else if f.clearChannel {
		empty := ""
		body.ChannelID = &empty
	}
	if body.BotToken == "" && body.KusoBotToken == "" && body.ChannelID == nil {
		return body, fmt.Errorf("nothing to change: pass --bot-token-file, --kuso-bot-token-file, --channel or --clear-channel")
	}
	return body, nil
}

var incidentAgentSetDiscordCmd = &cobra.Command{
	Use:   "set-discord",
	Short: "Configure the incident agent's Discord bot (token, kuso API token, channel)",
	Long: `Store the Discord bot wiring for incident threads. Each part is
optional: only what you pass changes, so you can rotate just the bot
token. Tokens are read from a file or stdin ("-"), never from argv.

  --bot-token-file       the Discord bot token
  --kuso-bot-token-file  a kuso API token the bot uses to call back
                         (mint one with 'kuso token create')
  --channel / --clear-channel   the Discord channel id for threads

Saving restarts the kuso-incident-bot deployment so it reconnects.
--dry-run shows what would be sent (tokens redacted) without saving.`,
	Example: `  pbpaste | kuso incident-agent set-discord --bot-token-file - --channel 1234567890
  kuso incident-agent set-discord --kuso-bot-token-file ./bot.jwt
  kuso incident-agent set-discord --clear-channel --dry-run`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		body, err := buildIncidentAgentDiscord(cmd)
		if err != nil {
			return err
		}
		var parts []string
		if body.BotToken != "" {
			parts = append(parts, "discord bot token")
		}
		if body.KusoBotToken != "" {
			parts = append(parts, "kuso bot token")
		}
		if body.ChannelID != nil {
			if *body.ChannelID == "" {
				parts = append(parts, "channel (cleared)")
			} else {
				parts = append(parts, "channel "+*body.ChannelID)
			}
		}
		if incidentAgentDiscord.dryRun {
			fmt.Printf("dry run: would update %s\n", strings.Join(parts, ", "))
			return nil
		}
		resp, err := api.PutIncidentAgentDiscord(body)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("save discord settings: %w", err)
		}
		fmt.Printf("updated %s; the incident bot is restarting\n", strings.Join(parts, ", "))
		return nil
	},
}

var incidentFeedback struct {
	text, file string
}

var incidentFeedbackCmd = &cobra.Command{
	Use:   "feedback <id>",
	Short: "Reply to an incident's findings (recorded on the incident)",
	Long: `Add an operator reply to an incident's feedback log, the same as
replying in its Discord thread. This only records text; it never approves
or rejects a proposed fix. Close an incident with 'kuso incident resolve'.`,
	Example: `  kuso incident feedback 4f2a --text "the OOM is from the nightly export, not traffic"
  kuso incident feedback 4f2a --file notes.md
  git log -3 | kuso incident feedback 4f2a --file -`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		f := &incidentFeedback
		if (f.text == "") == (f.file == "") {
			return fmt.Errorf("pass exactly one of --text or --file")
		}
		text := f.text
		if f.file != "" {
			v, err := readValueFrom(f.file)
			if err != nil {
				return fmt.Errorf("read --file: %w", err)
			}
			text = v
		}
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("feedback text is empty")
		}
		resp, err := api.IncidentFeedbackText(args[0], text)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("incident feedback: %w", err)
		}
		fmt.Printf("feedback recorded on incident %s\n", args[0])
		return nil
	},
}

func init() {
	incidentAgentCmd.AddCommand(incidentAgentSettingsCmd, incidentAgentSetDiscordCmd)
	incidentAgentSettingsCmd.AddCommand(incidentAgentSettingsGetCmd, incidentAgentSettingsSetCmd)
	incidentAgentSettingsGetCmd.Flags().StringVarP(&incidentAgentSettingsGetOutput, "output", "o", "table", "output format [table, json]")

	sf := incidentAgentSettingsSetCmd.Flags()
	s := &incidentAgentSettingsSet
	sf.BoolVar(&s.enabled, "enabled", false, "turn the agent on (--enabled=false turns it off)")
	sf.BoolVar(&s.triggerPod, "trigger-pod", false, "investigate pod crash events")
	sf.BoolVar(&s.triggerAlert, "trigger-alert", false, "investigate alert-rule firings")
	sf.BoolVar(&s.triggerNode, "trigger-node", false, "investigate node-unreachable events")
	sf.IntVar(&s.maxConcurrent, "max-concurrent", 0, "max investigations at once, 0..50 (0 = no limit)")
	sf.IntVar(&s.cooldownHours, "cooldown-hours", 0, "hours before the same target is investigated again, 0..168")
	sf.StringVar(&s.agentImage, "agent-image", "", "override the agent container image (empty = default)")
	sf.BoolVar(&s.dryRun, "dry-run", false, "print the resulting config without saving")

	df := incidentAgentSetDiscordCmd.Flags()
	d := &incidentAgentDiscord
	df.StringVar(&d.botTokenFile, "bot-token-file", "", "file holding the Discord bot token (- for stdin)")
	df.StringVar(&d.kusoBotTokenFile, "kuso-bot-token-file", "", "file holding the kuso API token the bot uses (- for stdin)")
	df.StringVar(&d.channel, "channel", "", "Discord channel id for incident threads")
	df.BoolVar(&d.clearChannel, "clear-channel", false, "remove the stored channel id")
	df.BoolVar(&d.dryRun, "dry-run", false, "show what would change without saving")

	incidentCmd.AddCommand(incidentFeedbackCmd)
	incidentFeedbackCmd.Flags().StringVar(&incidentFeedback.text, "text", "", "feedback text")
	incidentFeedbackCmd.Flags().StringVar(&incidentFeedback.file, "file", "", "read feedback text from a file (- for stdin)")
}
