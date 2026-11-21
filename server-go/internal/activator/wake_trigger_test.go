package activator

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWokeLogAttrs_NamesTheTrigger(t *testing.T) {
	r := httptest.NewRequest("HEAD", "https://app.example.com/health?x=1", nil)
	r.Header.Set("User-Agent", "UptimeRobot/2.0 "+strings.Repeat("x", 500))
	ctx := context.WithValue(context.Background(), wakeTriggerKey{}, r)

	got := fmt.Sprint(wokeLogAttrs(ctx, "ns", "web", 6127*time.Millisecond))
	for _, want := range []string{"readyAfter 6.127s", "method HEAD", "path /health", "userAgent UptimeRobot/2.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("attrs %q missing %q", got, want)
		}
	}
	if len(got) > 400 {
		t.Errorf("user agent not truncated: %d chars", len(got))
	}
	if bare := fmt.Sprint(wokeLogAttrs(context.Background(), "ns", "web", time.Second)); strings.Contains(bare, "userAgent") {
		t.Errorf("no request in ctx but attrs carry a user agent: %q", bare)
	}
}
