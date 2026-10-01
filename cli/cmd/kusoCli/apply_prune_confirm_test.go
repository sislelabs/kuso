package kusoCli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

// apply with prune: true used to POST straight away and delete whatever
// the file no longer lists. The plan is now fetched first and deletions
// need confirmation, which fails closed off a TTY.
func applyPlanServer(t *testing.T, plan string) *[]string {
	t.Helper()
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, plan)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	orig := stdinIsTTYFn
	stdinIsTTYFn = func() bool { return false }
	t.Cleanup(func() { api = nil; stdinIsTTYFn = orig })
	return &reqs
}

func TestConfirmApplyDeletes_RefusesPruneOffTTY(t *testing.T) {
	reqs := applyPlanServer(t, `{"servicesToDelete":["old"],"addonsToDelete":["db"]}`)
	err := confirmApplyDeletes("shop", []byte("project: shop\nprune: true\n"))
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("want refusal naming --yes, got %v", err)
	}
	if len(*reqs) != 1 || !strings.Contains((*reqs)[0], "dryRun=1") {
		t.Fatalf("only the dry-run plan may be requested, got %v", *reqs)
	}
}

func TestConfirmApplyDeletes_NoDeletesNoPrompt(t *testing.T) {
	applyPlanServer(t, `{"servicesToUpdate":["api"],"wouldDelete":["service:old"]}`)
	if err := confirmApplyDeletes("shop", []byte("project: shop\n")); err != nil {
		t.Fatalf("plan without deletions must not prompt: %v", err)
	}
}

func TestApplyRegistersYes(t *testing.T) {
	f := applyCmd.Flags().Lookup("yes")
	if f == nil || f.Shorthand != "y" {
		t.Fatalf("apply needs --yes/-y for non-interactive prune")
	}
}
