package handlers

// notifications_typeswitch_test.go covers the SSRF-validation bypass on
// notification updates. Validation used to be gated on body.Config !=
// nil, so a PUT carrying ONLY `type` switched the channel type while
// keeping a stored config that was never validated for the new type —
// e.g. telegram → webhook, where the stored config has no (or an
// unvetted) URL. The handler must always validate the EFFECTIVE
// (type, config) pair: the pair the update will actually store.

import (
	"strings"
	"testing"

	"kuso/server/internal/db"
)

func TestNotifUpdate_TypeSwitchWithoutConfigIsValidated(t *testing.T) {
	t.Parallel()
	existing := &db.Notification{
		Type:   "telegram",
		Config: map[string]any{"botToken": "123:abc", "chatId": "42"},
	}
	body := &notifBody{Type: "webhook"} // no config in the body

	typ := effectiveNotifType(body, existing)
	cfg := effectiveNotifConfig(body, existing)
	if typ != "webhook" {
		t.Fatalf("effective type = %q, want webhook", typ)
	}
	// The stored telegram config has no url — the switched-to webhook
	// type must fail validation rather than silently taking effect.
	if err := validateNotificationConfig(typ, cfg); err == nil {
		t.Fatal("type-switch over an incompatible stored config must fail validation")
	}
}

func TestNotifUpdate_TypeSwitchToSSRFTargetIsBlocked(t *testing.T) {
	t.Parallel()
	// A url-bearing key CAN pre-exist in the stored config (e.g. it was
	// stored under a type that never validates url). Switching to a
	// url-consuming type must run the SSRF guard on the effective pair.
	existing := &db.Notification{
		Type:   "telegram",
		Config: map[string]any{"botToken": "123:abc", "chatId": "42", "url": "http://169.254.169.254/latest/meta-data"},
	}
	body := &notifBody{Type: "webhook"}
	err := validateNotificationConfig(effectiveNotifType(body, existing), effectiveNotifConfig(body, existing))
	if err == nil {
		t.Fatal("IMDS-targeting stored url must fail the SSRF guard on type switch")
	}
	if !strings.Contains(err.Error(), "config.url") {
		t.Errorf("expected a config.url validation error, got: %v", err)
	}
}

func TestNotifUpdate_TypeSwitchWithValidConfigPasses(t *testing.T) {
	t.Parallel()
	existing := &db.Notification{
		Type:   "telegram",
		Config: map[string]any{"botToken": "123:abc", "chatId": "42"},
	}
	body := &notifBody{
		Type:   "webhook",
		Config: map[string]any{"url": "https://hooks.example.com/kuso"},
	}
	err := validateNotificationConfig(effectiveNotifType(body, existing), effectiveNotifConfig(body, existing))
	if err != nil {
		t.Fatalf("valid type switch with matching config must pass: %v", err)
	}
}

func TestNotifUpdate_NoTypeNoConfigValidatesStoredPair(t *testing.T) {
	t.Parallel()
	// A rename-only PUT re-validates the stored pair — a valid stored
	// channel keeps working.
	existing := &db.Notification{
		Type:   "pushover",
		Config: map[string]any{"token": "t", "user": "u"},
	}
	body := &notifBody{Name: "renamed"}
	err := validateNotificationConfig(effectiveNotifType(body, existing), effectiveNotifConfig(body, existing))
	if err != nil {
		t.Fatalf("rename over a valid stored pair must pass: %v", err)
	}
}
