package kusoCli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderProjectTop(t *testing.T) {
	rows := []projectTopRow{
		{Project: "shop", Workload: "shop-web-production", Pod: "web-1", Container: "app", Node: "n1", HasUsage: true,
			CPUm: 12, CPURequestm: 250, MemBytes: 100 << 20, MemRequest: 512 << 20, MemLimit: 1 << 30, Restarts: 2},
		{Project: "shop", Workload: "shop-db", Pod: "db-0", Container: "postgres", Node: "n2", MemRequest: 256 << 20},
	}
	if err := sortProjectTop(rows, "mem-request"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	renderProjectTop(&buf, rows, true)
	out := buf.String()
	web := strings.Index(out, "web-1/app")
	db := strings.Index(out, "db-0/postgres")
	if web < 0 || db < 0 || web > db {
		t.Fatalf("want web-1 (512Mi request) before db-0:\n%s", out)
	}
	for _, want := range []string{"12m", "250m", "100.0Mi", "512.0Mi", "1.0Gi", "SHOP"} {
		if !strings.Contains(strings.ToUpper(out), strings.ToUpper(want)) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if err := sortProjectTop(rows, "bogus"); err == nil {
		t.Error("unknown --sort key should error")
	}
}
