package kusoCli

import (
	"strings"
	"testing"
)

func TestProjectDeletedMsg(t *testing.T) {
	if got := projectDeletedMsg("e2e", true, nil); got != "project e2e deleted (PVCs purged)\n" {
		t.Errorf("no warnings: %q", got)
	}
	got := projectDeletedMsg("e2e", false, []byte(`{"warnings":["cleanup left orphans: Namespace kuso-e2e forbidden"]}`))
	if !strings.Contains(got, "WARNING: cleanup left orphans: Namespace kuso-e2e forbidden") {
		t.Errorf("warning not shown: %q", got)
	}
}
