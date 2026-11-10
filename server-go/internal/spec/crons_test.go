package spec

import "testing"

func TestCronCreateReq_MapsByKind(t *testing.T) {
	cmd := CronSpec{Name: "c", Kind: "command", Schedule: "0 2 * * *",
		Image: "alpine:3", Command: []string{"sh", "-c", "echo"}}
	req := cronCreateReq(cmd)
	if req.Name != "c" || req.Kind != "command" || req.Schedule != "0 2 * * *" {
		t.Fatalf("base fields wrong: %+v", req)
	}
	if req.Image == nil || req.Image.Repository != "alpine" || req.Image.Tag != "3" {
		t.Fatalf("command-kind image not mapped: %+v", req.Image)
	}

	httpc := CronSpec{Name: "h", Kind: "http", Schedule: "0 1 * * *", URL: "https://x"}
	hreq := cronCreateReq(httpc)
	if hreq.URL != "https://x" || hreq.Image != nil {
		t.Fatalf("http-kind mapping wrong: %+v", hreq)
	}
}

func TestParseCronScheduleMatchesCronRoutes(t *testing.T) {
	ok := []string{"@daily", "@hourly", "0 2 * * *", "0 9 * * mon-fri"}
	for _, s := range ok {
		raw := "project: p\ncrons:\n  - name: c\n    kind: http\n    url: https://x\n    schedule: \"" + s + "\"\n"
		if _, err := Parse([]byte(raw)); err != nil {
			t.Errorf("schedule %q rejected: %v", s, err)
		}
	}
	bad := []string{"@every 5m", "0 25 * * *", "* * * *"}
	for _, s := range bad {
		raw := "project: p\ncrons:\n  - name: c\n    kind: http\n    url: https://x\n    schedule: \"" + s + "\"\n"
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("schedule %q accepted, want error", s)
		}
	}
}
