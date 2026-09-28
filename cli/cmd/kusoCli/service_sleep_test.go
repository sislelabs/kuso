package kusoCli

import (
	"encoding/json"
	"testing"

	"kuso/pkg/kusoApi"
)

func TestParseSleepAfter(t *testing.T) {
	t.Parallel()
	ok := map[string]int{"30m": 30, "1h": 60, "90m": 90, "45": 45, "1h30m": 90}
	for in, want := range ok {
		got, err := parseSleepAfter(in)
		if err != nil || got != want {
			t.Errorf("parseSleepAfter(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "0m", "-5m", "30s", "90s", "abc", "1.5"} {
		if _, err := parseSleepAfter(bad); err == nil {
			t.Errorf("parseSleepAfter(%q) should fail", bad)
		}
	}
}

func TestBuildSleepPatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, state, after, nonProd string
		want                        string
		wantErr                     bool
	}{
		{"on", "on", "", "", `{"sleep":{"enabled":true}}`, false},
		{"off", "off", "", "", `{"sleep":{"enabled":false}}`, false},
		{"on with window", "on", "15m", "", `{"sleep":{"enabled":true,"afterMinutes":15}}`, false},
		{"non-prod only", "", "", "off", `{"sleep":{"nonProduction":"off"}}`, false},
		{"everything", "off", "1h", "on", `{"sleep":{"enabled":false,"afterMinutes":60,"nonProduction":"on"}}`, false},
		{"bad state", "maybe", "", "", "", true},
		{"bad non-prod", "on", "", "sometimes", "", true},
		{"bad window", "on", "10s", "", "", true},
		{"nothing to change", "", "", "", "", true},
	}
	for _, c := range cases {
		req, err := buildSleepPatch(c.state, c.after, c.nonProd)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: want error, got %+v", c.name, req)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		b, _ := json.Marshal(kusoApi.PatchSleepBody{Sleep: req})
		if string(b) != c.want {
			t.Errorf("%s: body = %s, want %s", c.name, b, c.want)
		}
	}
}
