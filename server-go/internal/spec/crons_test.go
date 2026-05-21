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
