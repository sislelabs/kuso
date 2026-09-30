package updater

import (
	"context"

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
