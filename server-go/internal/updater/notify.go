package updater

import (
	"context"
	"strings"
	"time"

	"kuso/server/internal/notify"
)

// EventEmitter is the slice of notify.Dispatcher the updater needs.
type EventEmitter interface {
	Emit(notify.Event)
}

type settingStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value, updatedBy string) error
}

// notifiedVersionKey records the last version an update.available event
// went out for, in the Setting kv so restarts and leader changes don't
// re-announce the same release.
const notifiedVersionKey = "updater.updateAvailable.notifiedVersion"

const updatesURL = "/settings/updates"

func (s *Service) settingStore() settingStore {
	if s.settings != nil {
		return s.settings
	}
	if s.DB != nil {
		return s.DB
	}
	return nil
}

// maybeNotifyAvailable emits update.available at most once per version.
func (s *Service) maybeNotifyAvailable(ctx context.Context, st State) {
	store := s.settingStore()
	if s.Notify == nil || store == nil || !st.NeedsUpdate || st.Latest == "" {
		return
	}
	last, _ := store.GetSetting(ctx, notifiedVersionKey)
	if last == st.Latest {
		return
	}
	// Record first: a failed write must not turn into an event per poll.
	if err := store.SetSetting(ctx, notifiedVersionKey, st.Latest, "updater"); err != nil {
		if s.Logger != nil {
			s.Logger.Warn("updater: record notified version", "err", err)
		}
		return
	}
	body := "kuso " + st.Latest + " is available (running " + st.Current + "). Nothing installs until an admin presses Update."
	if st.Manifest != nil && st.Manifest.Breaking {
		body += "\nThis release is marked breaking — read the notes first."
	}
	s.Notify.Emit(notify.Event{
		Type:        notify.EventUpdateAvailable,
		Title:       "kuso " + st.Latest + " available",
		Description: body,
		Body:        body,
		URL:         updatesURL,
		Severity:    "info",
		Links:       []notify.EventLink{{Label: "Updates", URL: updatesURL}},
	})
}

// Setting keys deduping the outcome / rejection events across restarts
// and leader changes.
const (
	notifiedOutcomeKey  = "updater.outcome.notifiedRun"
	notifiedRejectedKey = "updater.rejected.notifiedVersion"
)

// outcomeMaxAge: a terminal status older than this when first seen (a
// server that just gained this check, a restored DB) is recorded but not
// announced, so an upgrade never replays a months-old failure.
const outcomeMaxAge = 2 * time.Hour

// outcomeEvent maps a terminal updater status to the event it raises, or
// "" for a clean success / a phase still in progress.
func outcomeEvent(st UpdateStatus) (notify.EventType, string) {
	switch st.Phase {
	case "failed", "rolled-back", "rollback-failed":
		return notify.EventUpdateFailed, "error"
	case "done":
		if strings.Contains(st.Message, "with warnings") {
			return notify.EventUpdatePartial, "warn"
		}
	}
	return "", ""
}

// checkUpdateOutcome emits update.failed / update.partial once per
// updater run, read from the status ConfigMap the Job writes.
func (s *Service) checkUpdateOutcome(ctx context.Context) {
	if s.Notify == nil || s.Kube == nil {
		return
	}
	st, ok := s.Status(ctx)
	if !ok {
		return
	}
	s.notifyOutcome(ctx, st, time.Now())
}

func (s *Service) notifyOutcome(ctx context.Context, st UpdateStatus, now time.Time) {
	store := s.settingStore()
	if s.Notify == nil || store == nil || !terminalPhase(st.Phase) {
		return
	}
	run := st.Started.UTC().Format(time.RFC3339) + " " + st.Phase
	last, _ := store.GetSetting(ctx, notifiedOutcomeKey)
	if last == run {
		return
	}
	if err := store.SetSetting(ctx, notifiedOutcomeKey, run, "updater"); err != nil {
		if s.Logger != nil {
			s.Logger.Warn("updater: record notified outcome", "err", err)
		}
		return
	}
	typ, sev := outcomeEvent(st)
	if typ == "" || (!st.Updated.IsZero() && now.Sub(st.Updated) > outcomeMaxAge) {
		return
	}
	title := "kuso update failed"
	if st.Phase == "rolled-back" {
		title = "kuso update rolled back"
	}
	if typ == notify.EventUpdatePartial {
		title = "kuso update finished with manual steps owed"
	}
	s.Notify.Emit(notify.Event{
		Type:        typ,
		Title:       title,
		Description: st.Message,
		Body:        st.Message,
		URL:         updatesURL,
		Severity:    sev,
		Links:       []notify.EventLink{{Label: "Updates", URL: updatesURL}},
	})
}

// notifyRejected emits update.rejected once per release that failed
// signature / digest verification.
func (s *Service) notifyRejected(ctx context.Context, tag, reason string) {
	store := s.settingStore()
	if s.Notify == nil || store == nil || tag == "" {
		return
	}
	if last, _ := store.GetSetting(ctx, notifiedRejectedKey); last == tag {
		return
	}
	if err := store.SetSetting(ctx, notifiedRejectedKey, tag, "updater"); err != nil {
		return
	}
	body := "kuso " + tag + " was refused: " + reason + ". Nothing was installed. A release that fails verification may have been tampered with; check the release on GitHub before overriding anything."
	s.Notify.Emit(notify.Event{
		Type:        notify.EventUpdateRejected,
		Title:       "kuso " + tag + " failed verification",
		Description: body,
		Body:        body,
		URL:         updatesURL,
		Severity:    "error",
		Links:       []notify.EventLink{{Label: "Updates", URL: updatesURL}},
	})
}
