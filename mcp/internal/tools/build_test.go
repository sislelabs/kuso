package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// OPS-1: the server sets existing=true when a trigger coalesced into an
// in-flight build; the tool used to report that as "triggered".
func TestBuildTriggerTextHonoursExisting(t *testing.T) {
	var out buildSummary
	if err := json.Unmarshal([]byte(`{"id":"b1","status":"running","branch":"main","existing":true}`), &out); err != nil {
		t.Fatal(err)
	}
	text := buildTriggerText(out)
	if strings.Contains(text, "triggered") || !strings.Contains(text, "coalesced into b1") {
		t.Errorf("coalesced trigger text = %q", text)
	}
	out.Existing = false
	if text := buildTriggerText(out); !strings.HasPrefix(text, "build b1 triggered") {
		t.Errorf("fresh trigger text = %q", text)
	}
}
