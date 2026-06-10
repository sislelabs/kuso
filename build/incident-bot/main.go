// Command incident-bot is the two-way Discord bridge for kuso's autonomous
// incident-response agent. It is a small, standalone deployable (its own
// go.mod) so the discordgo dependency never leaks into server-go.
//
// What it does:
//   - Connects to Discord with a bot token (DISCORD_BOT_TOKEN) and watches one
//     configured channel (DISCORD_CHANNEL_ID).
//   - Polls the kuso API (KUSO_API_URL, authed with KUSO_BOT_TOKEN) on a ticker
//     for incidents in state awaiting_feedback. For any such incident that has
//     no Discord thread yet, it opens a thread in the watched channel, posts the
//     agent's findings, then records the thread id back into kuso via
//     POST /api/incidents/{id}/thread.
//   - Listens for messages and reactions inside incident threads it owns:
//       * a plain message            -> POST /feedback {text:"..."}
//       * "go" (or a ✅ reaction)     -> POST /feedback {decision:"go"}
//       * "reject" (or a ❌ reaction) -> POST /feedback {decision:"reject"}
//   - Maps Discord thread id <-> incident id in an in-memory table that is
//     rebuilt from the kuso API on start (so a bot restart re-adopts threads).
//
// The pure message->action decision lives in messageToAction (see main_test.go).
//
// See docs/superpowers/specs/2026-06-10-incident-agent-design.md (component 5).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Reaction emoji the bot treats as approve / reject. Plain unicode so they
// match what a phone or desktop Discord client sends by default.
const (
	emojiGo     = "✅"
	emojiReject = "❌"
)

// pollInterval is how often we sweep the API for incidents that need a thread.
const pollInterval = 15 * time.Second

// config is the environment-sourced runtime configuration.
type config struct {
	DiscordToken string // DISCORD_BOT_TOKEN
	ChannelID    string // DISCORD_CHANNEL_ID — where threads are created
	KusoAPIURL   string // KUSO_API_URL  e.g. http://kuso-server.kuso:3000
	KusoBotToken string // KUSO_BOT_TOKEN — bearer for the incidents API
}

func loadConfig() (config, error) {
	c := config{
		DiscordToken: os.Getenv("DISCORD_BOT_TOKEN"),
		ChannelID:    os.Getenv("DISCORD_CHANNEL_ID"),
		KusoAPIURL:   strings.TrimRight(os.Getenv("KUSO_API_URL"), "/"),
		KusoBotToken: os.Getenv("KUSO_BOT_TOKEN"),
	}
	var missing []string
	if c.DiscordToken == "" {
		missing = append(missing, "DISCORD_BOT_TOKEN")
	}
	if c.ChannelID == "" {
		missing = append(missing, "DISCORD_CHANNEL_ID")
	}
	if c.KusoAPIURL == "" {
		missing = append(missing, "KUSO_API_URL")
	}
	if c.KusoBotToken == "" {
		missing = append(missing, "KUSO_BOT_TOKEN")
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

// incident is the subset of kuso's Incident JSON the bot needs. Field tags
// mirror server-go/internal/db/incidents.go's JSON shape.
type incident struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	Title         string `json:"title"`
	Severity      string `json:"severity"`
	Project       string `json:"project"`
	Service       string `json:"service"`
	Findings      string `json:"findings"`
	DiscordThread string `json:"discordThread"`
	PRUrl         string `json:"prUrl"`
}

// bot holds the live Discord session, kuso client, config, and the
// thread<->incident map.
type bot struct {
	cfg  config
	dg   *discordgo.Session
	kuso *kusoClient
	log  *slog.Logger

	mu        sync.RWMutex
	byThread  map[string]string // discord thread id -> incident id
	hasThread map[string]bool   // incident id -> already threaded
}

func newBot(cfg config, dg *discordgo.Session, log *slog.Logger) *bot {
	return &bot{
		cfg:       cfg,
		dg:        dg,
		kuso:      newKusoClient(cfg.KusoAPIURL, cfg.KusoBotToken),
		log:       log,
		byThread:  map[string]string{},
		hasThread: map[string]bool{},
	}
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := loadConfig()
	if err != nil {
		log.Error("incident-bot: config", "err", err)
		os.Exit(1)
	}

	dg, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		log.Error("incident-bot: discord session", "err", err)
		os.Exit(1)
	}
	// We need message content + reactions; guild + message intents cover
	// thread messages and reaction add events in the watched channel.
	dg.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildMessageReactions |
		discordgo.IntentMessageContent

	b := newBot(cfg, dg, log)
	dg.AddHandler(b.onMessageCreate)
	dg.AddHandler(b.onReactionAdd)

	if err := dg.Open(); err != nil {
		log.Error("incident-bot: discord open", "err", err)
		os.Exit(1)
	}
	defer dg.Close()
	log.Info("incident-bot: connected", "channel", cfg.ChannelID, "api", cfg.KusoAPIURL)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Rebuild the thread<->incident map from the API so a restart re-adopts
	// threads it created in a previous life. Best-effort; the poll loop will
	// fill any gaps.
	if err := b.rebuildMap(ctx); err != nil {
		log.Warn("incident-bot: rebuild map (continuing)", "err", err)
	}

	b.pollLoop(ctx)
	log.Info("incident-bot: shutting down")
}

// pollLoop sweeps awaiting_feedback incidents on a ticker until ctx is done.
func (b *bot) pollLoop(ctx context.Context) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	// One immediate pass so we don't wait a full interval on boot.
	b.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.sweep(ctx)
		}
	}
}

// sweep posts threads for any awaiting_feedback incident that lacks one.
func (b *bot) sweep(ctx context.Context) {
	incs, err := b.kuso.listIncidents(ctx, "awaiting_feedback")
	if err != nil {
		b.log.Warn("incident-bot: list incidents", "err", err)
		return
	}
	for _, in := range incs {
		if in.DiscordThread != "" {
			// Server already knows the thread; just make sure we route it.
			b.remember(in.DiscordThread, in.ID)
			continue
		}
		b.mu.RLock()
		done := b.hasThread[in.ID]
		b.mu.RUnlock()
		if done {
			continue
		}
		if err := b.openThread(ctx, in); err != nil {
			b.log.Error("incident-bot: open thread", "incident", in.ID, "err", err)
		}
	}
}

// openThread creates a Discord thread for the incident, posts the findings,
// and records the thread id back into kuso.
func (b *bot) openThread(ctx context.Context, in incident) error {
	name := threadName(in)
	th, err := b.dg.ThreadStart(b.cfg.ChannelID, name, discordgo.ChannelTypeGuildPublicThread, 1440)
	if err != nil {
		return fmt.Errorf("thread start: %w", err)
	}
	// Mark immediately so a slow API round-trip can't double-create on the
	// next tick.
	b.remember(th.ID, in.ID)

	body := findingsMessage(in)
	for _, chunk := range splitForDiscord(body) {
		if _, err := b.dg.ChannelMessageSend(th.ID, chunk); err != nil {
			b.log.Warn("incident-bot: post findings chunk", "incident", in.ID, "err", err)
		}
	}
	// Seed the approve/reject reactions so the operator can one-tap.
	_ = b.dg.MessageReactionAdd(th.ID, "", emojiGo)

	if err := b.kuso.setThread(ctx, in.ID, th.ID); err != nil {
		// Non-fatal: the bot's in-memory map still routes replies. The next
		// sweep won't re-create because we remembered the thread above.
		b.log.Warn("incident-bot: record thread in kuso", "incident", in.ID, "err", err)
	}
	b.log.Info("incident-bot: thread opened", "incident", in.ID, "thread", th.ID)
	return nil
}

// rebuildMap repopulates byThread from every incident the API knows about
// that already carries a discordThread.
func (b *bot) rebuildMap(ctx context.Context) error {
	incs, err := b.kuso.listIncidents(ctx, "")
	if err != nil {
		return err
	}
	for _, in := range incs {
		if in.DiscordThread != "" {
			b.remember(in.DiscordThread, in.ID)
		}
	}
	b.log.Info("incident-bot: map rebuilt", "threads", len(incs))
	return nil
}

func (b *bot) remember(threadID, incidentID string) {
	b.mu.Lock()
	b.byThread[threadID] = incidentID
	b.hasThread[incidentID] = true
	b.mu.Unlock()
}

func (b *bot) incidentFor(threadID string) (string, bool) {
	b.mu.RLock()
	id, ok := b.byThread[threadID]
	b.mu.RUnlock()
	return id, ok
}

// onMessageCreate handles plain text + "go"/"reject" keywords in our threads.
func (b *bot) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot {
		return // ignore our own + other bots' messages
	}
	incidentID, ok := b.incidentFor(m.ChannelID)
	if !ok {
		return // not an incident thread we own
	}
	action := messageToAction(m.Content, "")
	b.dispatch(context.Background(), incidentID, action, m.Content)
}

// onReactionAdd handles ✅ / ❌ reactions on messages in our threads.
func (b *bot) onReactionAdd(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
	if s.State != nil && s.State.User != nil && r.UserID == s.State.User.ID {
		return // ignore the seed reaction the bot itself added
	}
	incidentID, ok := b.incidentFor(r.ChannelID)
	if !ok {
		return
	}
	action := messageToAction("", r.Emoji.Name)
	if action == "" {
		return // some other emoji; ignore
	}
	b.dispatch(context.Background(), incidentID, action, "")
}

// dispatch turns a resolved action into the right kuso feedback POST.
func (b *bot) dispatch(ctx context.Context, incidentID, action, text string) {
	switch action {
	case "go":
		if err := b.kuso.feedbackDecision(ctx, incidentID, "go"); err != nil {
			b.log.Error("incident-bot: post go", "incident", incidentID, "err", err)
		} else {
			b.log.Info("incident-bot: approved", "incident", incidentID)
		}
	case "reject":
		if err := b.kuso.feedbackDecision(ctx, incidentID, "reject"); err != nil {
			b.log.Error("incident-bot: post reject", "incident", incidentID, "err", err)
		} else {
			b.log.Info("incident-bot: rejected", "incident", incidentID)
		}
	case "text":
		if strings.TrimSpace(text) == "" {
			return
		}
		if err := b.kuso.feedbackText(ctx, incidentID, text); err != nil {
			b.log.Error("incident-bot: post feedback", "incident", incidentID, "err", err)
		} else {
			b.log.Info("incident-bot: feedback relayed", "incident", incidentID)
		}
	}
}

// messageToAction maps an inbound Discord signal to one of three actions:
// "go", "reject", or "text". Exactly one of content / reaction is meaningful
// per call (the other is ""). This is the pure decision tested in main_test.go.
//
// Rules (case/space-insensitive for keywords):
//   - reaction ✅  -> "go"      reaction ❌ -> "reject"   other reaction -> ""
//   - content "go"/"approve"/"ship"/"yes"   -> "go"
//   - content "reject"/"no"/"cancel"/"stop" -> "reject"
//   - any other non-empty content           -> "text"
//   - empty content + empty reaction         -> ""  (nothing to do)
func messageToAction(content, reaction string) string {
	switch reaction {
	case emojiGo:
		return "go"
	case emojiReject:
		return "reject"
	case "":
		// fall through to content handling
	default:
		return "" // an emoji we don't care about
	}

	c := strings.ToLower(strings.TrimSpace(content))
	if c == "" {
		return ""
	}
	switch c {
	case "go", "approve", "ship", "yes", "lgtm", "👍":
		return "go"
	case "reject", "no", "cancel", "stop", "abort":
		return "reject"
	}
	return "text"
}

// --- presentation helpers ---

func threadName(in incident) string {
	loc := in.Project
	if in.Service != "" {
		loc += "/" + in.Service
	}
	title := in.Title
	if title == "" {
		title = in.ID
	}
	name := fmt.Sprintf("incident: %s — %s", loc, title)
	return truncate(name, 100) // Discord thread names cap at 100 chars
}

func findingsMessage(in incident) string {
	var sb strings.Builder
	sev := in.Severity
	if sev == "" {
		sev = "warn"
	}
	fmt.Fprintf(&sb, "**Incident %s** (%s)\n", in.ID, sev)
	loc := in.Project
	if in.Service != "" {
		loc += "/" + in.Service
	}
	if loc != "" {
		fmt.Fprintf(&sb, "Target: `%s`\n", loc)
	}
	sb.WriteString("\n")
	if in.Findings != "" {
		sb.WriteString(in.Findings)
	} else {
		sb.WriteString("_(no findings yet)_")
	}
	sb.WriteString("\n\n— React ✅ / say `go` to approve a fix PR, ❌ / `reject` to dismiss, or reply with extra context.")
	return sb.String()
}

// splitForDiscord chunks a message into <=2000-char pieces (Discord's hard
// per-message limit), preferring to break on newlines.
func splitForDiscord(s string) []string {
	const max = 1900 // leave headroom under the 2000 hard cap
	if len(s) <= max {
		return []string{s}
	}
	var out []string
	for len(s) > max {
		cut := strings.LastIndex(s[:max], "\n")
		if cut <= 0 {
			cut = max
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if len(s) > 0 {
		out = append(out, s)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// --- kuso API client ---

// kusoClient is a tiny bearer-authed HTTP client for the incidents API.
type kusoClient struct {
	base  string
	token string
	hc    *http.Client
}

func newKusoClient(base, token string) *kusoClient {
	return &kusoClient{base: base, token: token, hc: &http.Client{Timeout: 20 * time.Second}}
}

// listIncidents GETs /api/incidents, optionally filtered by ?state=.
func (k *kusoClient) listIncidents(ctx context.Context, state string) ([]incident, error) {
	url := k.base + "/api/incidents"
	if state != "" {
		url += "?state=" + state
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	k.auth(req)
	resp, err := k.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusErr("list incidents", resp)
	}
	var out []incident
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode incidents: %w", err)
	}
	return out, nil
}

// setThread POSTs /api/incidents/{id}/thread {thread:"..."}.
func (k *kusoClient) setThread(ctx context.Context, id, threadID string) error {
	return k.post(ctx, fmt.Sprintf("/api/incidents/%s/thread", id),
		map[string]string{"thread": threadID})
}

// feedbackText POSTs /api/incidents/{id}/feedback {text:"..."}.
func (k *kusoClient) feedbackText(ctx context.Context, id, text string) error {
	return k.post(ctx, fmt.Sprintf("/api/incidents/%s/feedback", id),
		map[string]string{"text": text})
}

// feedbackDecision POSTs /api/incidents/{id}/feedback {decision:"go"|"reject"}.
func (k *kusoClient) feedbackDecision(ctx context.Context, id, decision string) error {
	return k.post(ctx, fmt.Sprintf("/api/incidents/%s/feedback", id),
		map[string]string{"decision": decision})
}

func (k *kusoClient) post(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	k.auth(req)
	resp, err := k.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return statusErr("POST "+path, resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (k *kusoClient) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+k.token)
}

func statusErr(what string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("%s: http %d: %s", what, resp.StatusCode, strings.TrimSpace(string(b)))
}
