package kusoCli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"kuso/pkg/kusoApi"
)

// fakeClock makes every wait instant: sleep advances the clock instead of
// blocking, so deadline and grace-period logic still runs.
func fakeClock(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	pollNow = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	pollSleep = func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	t.Cleanup(func() { pollNow = time.Now; pollSleep = time.Sleep })
}

// scripted serves one canned response per request, repeating the last.
// A status of 0 drops the connection (what a client sees while the
// server pod is replaced).
type scriptedResp struct {
	status int
	body   string
}

func scriptedServer(t *testing.T, script []scriptedResp) *int {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := calls
		calls++
		mu.Unlock()
		if i >= len(script) {
			i = len(script) - 1
		}
		s := script[i]
		if s.status == 0 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("no hijacker")
			}
			c, _, _ := hj.Hijack()
			_ = c.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil })
	return &calls
}

const runningBuild = `[{"id":"b1","status":"running"}]`

func TestPollBuildToTerminal_SurvivesServerRestart(t *testing.T) {
	fakeClock(t)
	scriptedServer(t, []scriptedResp{
		{200, runningBuild},
		{0, ""},
		{502, "bad gateway"},
		{503, "unavailable"},
		{200, `[{"id":"b1","status":"succeeded"}]`},
	})
	status, err := pollBuildToTerminal("p", "s", "b1", 20*time.Minute)
	if err != nil || status != "succeeded" {
		t.Fatalf("status=%q err=%v, want succeeded", status, err)
	}
}

func TestPollBuildToTerminal_GivesUpOnSustainedOutage(t *testing.T) {
	fakeClock(t)
	calls := scriptedServer(t, []scriptedResp{{200, runningBuild}, {502, "bad gateway"}})
	_, err := pollBuildToTerminal("p", "s", "b1", 20*time.Minute)
	if err == nil || !strings.Contains(err.Error(), "in a row failed") {
		t.Fatalf("err = %v, want a sustained-failure error", err)
	}
	if *calls != 25 {
		t.Fatalf("calls = %d, want 1 ok + 24 failures", *calls)
	}
}

func TestPollBuildToTerminal_ClientErrorFailsFast(t *testing.T) {
	fakeClock(t)
	calls := scriptedServer(t, []scriptedResp{{404, `{"error":"service not found"}`}})
	if _, err := pollBuildToTerminal("p", "s", "b1", 20*time.Minute); err == nil {
		t.Fatal("want error on 404")
	}
	if *calls != 1 {
		t.Fatalf("calls = %d, want 1", *calls)
	}
}

func TestPollBuildToTerminal_MissingBuildFails(t *testing.T) {
	fakeClock(t)
	scriptedServer(t, []scriptedResp{{200, `[{"id":"other","status":"running"}]`}})
	_, err := pollBuildToTerminal("p", "s", "b1", 20*time.Minute)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not-found well before the 20m deadline", err)
	}
}

func TestPollBuildToTerminal_TimeoutNamesReattach(t *testing.T) {
	fakeClock(t)
	scriptedServer(t, []scriptedResp{{200, runningBuild}})
	_, err := pollBuildToTerminal("p", "s", "b1", time.Minute)
	if err == nil || !strings.Contains(err.Error(), "kuso build wait p s b1") {
		t.Fatalf("err = %v, want a timeout naming the re-attach command", err)
	}
}

func TestClassifyBuildStatus(t *testing.T) {
	cases := []struct {
		status       string
		wantTerminal bool
		wantErr      bool
	}{
		{"pending", false, false},
		{"queued", false, false},
		{"running", false, false},
		{"", false, false},
		{"succeeded", true, false},
		{"failed", true, true},
		{"error", true, true},
		{"cancelled", true, true},
		{"superseded", true, true},
		{"timeout", true, true},
		{"release-failed", true, true},
	}
	for _, c := range cases {
		terminal, err := classifyBuildStatus("b1", buildSummaryRow{ID: "b1", Status: c.status})
		if terminal != c.wantTerminal || (err != nil) != c.wantErr {
			t.Errorf("%q: terminal=%v err=%v, want terminal=%v wantErr=%v", c.status, terminal, err, c.wantTerminal, c.wantErr)
		}
	}
}

func TestPickBuildToWait(t *testing.T) {
	items := []buildSummaryRow{{ID: "new", Status: "succeeded"}, {ID: "mid", Status: "running"}, {ID: "old", Status: "failed"}}
	if b, ok := pickBuildToWait(items); !ok || b.ID != "mid" {
		t.Fatalf("got %+v, want the in-flight build", b)
	}
	if b, ok := pickBuildToWait(items[:1]); !ok || b.ID != "new" {
		t.Fatalf("got %+v, want the newest build when none is running", b)
	}
	if _, ok := pickBuildToWait(nil); ok {
		t.Fatal("want no build for an empty list")
	}
}

func TestWaitForUpgrade_SurvivesServerRoll(t *testing.T) {
	fakeClock(t)
	scriptedServer(t, []scriptedResp{
		{200, `{"phase":"rolling-server"}`},
		{0, ""},
		{502, "bad gateway"},
		{200, `{"phase":"verifying-releases"}`},
		{200, `{"phase":"done","message":"upgraded to v9"}`},
	})
	if err := waitForUpgrade(20 * time.Minute); err != nil {
		t.Fatalf("err = %v, want success", err)
	}
}

func TestWaitForUpgrade_FailedPhaseIsError(t *testing.T) {
	fakeClock(t)
	scriptedServer(t, []scriptedResp{{200, `{"phase":"failed","message":"crd dry-run failed"}`}})
	err := waitForUpgrade(20 * time.Minute)
	if err == nil || !strings.Contains(err.Error(), "crd dry-run failed") {
		t.Fatalf("err = %v, want the failure message", err)
	}
}

func TestWaitForUpgrade_TimeoutIsReported(t *testing.T) {
	fakeClock(t)
	scriptedServer(t, []scriptedResp{{200, `{"phase":"verifying-releases"}`}})
	err := waitForUpgrade(time.Minute)
	if !errors.Is(err, errPollTimeout) || !strings.Contains(err.Error(), "upgrade status --wait") {
		t.Fatalf("err = %v, want a timeout naming the re-attach command", err)
	}
}
