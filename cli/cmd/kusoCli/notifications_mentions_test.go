package kusoCli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func withNotifFlags(t *testing.T, name, url, mention string, on []string, configFile string) {
	t.Helper()
	orig := []any{notifName, notifURL, notifMention, notifMentionOn, notifEditConfig}
	notifName, notifURL, notifMention, notifMentionOn, notifEditConfig = name, url, mention, on, configFile
	t.Cleanup(func() {
		notifName = orig[0].(string)
		notifURL = orig[1].(string)
		notifMention = orig[2].(string)
		notifMentionOn = orig[3].([]string)
		notifEditConfig = orig[4].(string)
	})
}

// The server's notify.mentionFor reads only config.mentions ({event: rule},
// rules "@here" / "@everyone" / "role:<id>" / "none"). The CLI used to write
// config.mention + config.mentionOn, which nothing read.
func TestBuildNotifBody_Mentions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mention string
		on      []string
		want    map[string]any // nil: must error
	}{
		{"WritesServerMentionsMap", "here", []string{"build.failed", "pod.crashed"},
			map[string]any{"build.failed": "@here", "pod.crashed": "@here"}},
		{"MentionWithoutEventsIsWildcard", "123456", nil, map[string]any{"*": "role:123456"}},
		// a mention the server can't render
		{"RejectsUnknownMention", "ops-team", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withNotifFlags(t, "disco", "https://discord.example/hook", tc.mention, tc.on, "")
			body, err := buildNotifBody("discord")
			if tc.want == nil {
				if err == nil {
					t.Fatal("want an error for a mention the server can't render")
				}
				return
			}
			if err != nil {
				t.Fatalf("buildNotifBody: %v", err)
			}
			if got := body.Config["mentions"]; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("config.mentions = %#v, want %#v", got, tc.want)
			}
			if _, ok := body.Config["mention"]; ok {
				t.Fatalf("legacy config.mention key still written: %#v", body.Config)
			}
		})
	}
}

// telegram/pushover/email were rejected by the type switch before
// --config-file was even read, so they couldn't be created from the CLI.
func TestBuildNotifBody_ConfigFileTypes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tg.json")
	if err := os.WriteFile(path, []byte(`{"botToken":"t","chatId":"42"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withNotifFlags(t, "tg", "", "", nil, path)
	body, err := buildNotifBody("telegram")
	if err != nil {
		t.Fatalf("telegram with --config-file: %v", err)
	}
	if body.Config["chatId"] != "42" {
		t.Fatalf("config not loaded: %#v", body.Config)
	}

	withNotifFlags(t, "tg", "", "", nil, "")
	if _, err := buildNotifBody("telegram"); err == nil {
		t.Fatal("telegram without --config-file should explain what's needed")
	}
}
