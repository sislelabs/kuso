package spec

import (
	"errors"
	"strings"
	"testing"
)

// kind=service used to pass Parse and plan, then fail at apply because the
// project cron routes take only http/command.
func TestParse_CronKindsMatchApply(t *testing.T) {
	cases := map[string]struct {
		cron string
		ok   bool
	}{
		"service":         {`{ name: n, kind: service, schedule: "0 3 * * *", service: api }`, false},
		"http no url":     {`{ name: n, kind: http, schedule: "0 3 * * *" }`, false},
		"command no argv": {`{ name: n, kind: command, schedule: "0 3 * * *", image: "r/i:1" }`, false},
		"http":            {`{ name: n, kind: http, schedule: "0 3 * * *", url: "https://x.example.com/hook" }`, true},
		"command":         {`{ name: n, kind: command, schedule: "0 3 * * *", image: "r/i:1", command: [run] }`, true},
	}
	for name, tc := range cases {
		_, err := Parse([]byte("project: p\ncrons:\n  - " + tc.cron + "\n"))
		if tc.ok && err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	_, err := Parse([]byte("project: p\ncrons:\n  - " + cases["service"].cron + "\n"))
	if err == nil || !strings.Contains(err.Error(), "kuso cron add") {
		t.Errorf("service-kind error should point at `kuso cron add`, got %v", err)
	}
}
