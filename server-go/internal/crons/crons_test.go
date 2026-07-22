package crons

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSchedule(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantOK  bool
		wantSub string
	}{
		// Standard 5-field grammar — should all pass.
		{"every-15m", "*/15 * * * *", true, ""},
		{"midnight", "0 0 * * *", true, ""},
		{"weekday", "0 9 * * 1-5", true, ""},
		{"step-and-list", "*/5,30 * * * *", true, ""},
		{"explicit-range", "0 0-23 1-31 1-12 0-6", true, ""},

		// Empty.
		{"empty", "", false, "required"},
		{"whitespace-only", "   ", false, "required"},

		// Quartz `?` — pass-4 P1-6 flagged this as the "validator
		// lies" bug. kube CronJob rejects it; we should too, with a
		// clear error, before it ever reaches the apiserver.
		{"quartz-dom-q", "0 0 ? * *", false, "5-field"},
		{"quartz-dow-q", "0 0 1 * ?", false, "5-field"},

		// @-macro shorthand — different dialect, kube rejects.
		{"at-hourly", "@hourly", false, "macro"},
		{"at-daily", "@daily", false, "macro"},
		{"at-yearly", "@yearly", false, "macro"},

		// 6-field (Quartz/Vixie with seconds).
		{"6-field-seconds", "0 0 0 * * *", false, "5-field"},

		// 4-field — not a valid schedule.
		{"4-field", "0 0 * *", false, "5-field"},

		// Wrong char.
		{"alpha", "every minute", false, "5-field"},
		{"semicolon", "0 0 * * *;rm", false, "5-field"},

		// Out-of-range values that PASS the shape regex but kube CronJob
		// rejects at reconcile (the "validator lies" bug). Must be caught
		// inline via per-field range checks.
		{"hour-25", "0 25 * * *", false, "range"},
		{"good-3am", "0 3 * * *", true, ""},
		{"minute-60", "60 * * * *", false, "range"},
		{"dom-0", "0 0 0 * *", false, "range"},
		{"dom-32", "0 0 32 * *", false, "range"},
		{"month-13", "0 0 1 13 *", false, "range"},
		{"dow-7", "0 0 * * 7", false, "range"},
		{"range-endpoint-oob", "0 0-24 * * *", false, "range"},
		{"list-elem-oob", "0 1,25 * * *", false, "range"},
		{"zero-step", "*/0 * * * *", false, "range"},
	}
	for _, c := range cases {
		err := validateSchedule(c.in)
		gotOK := err == nil
		if gotOK != c.wantOK {
			t.Errorf("%s: validateSchedule(%q) ok=%v, want %v (err=%v)", c.name, c.in, gotOK, c.wantOK, err)
			continue
		}
		if !c.wantOK {
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: error should wrap ErrInvalid; got %v", c.name, err)
			}
			if c.wantSub != "" && !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("%s: error %q missing %q", c.name, err.Error(), c.wantSub)
			}
		}
	}
}
