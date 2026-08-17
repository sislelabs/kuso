package kusoCli

import (
	"bytes"
	"strings"
	"testing"
)

// `build latest` and `db rows` both advertised `[table, json]` on -o
// but unconditionally emitted JSON. The table renderings now exist;
// these tests pin the row/render helpers they're built on so the
// advertised format stays honored.

func TestBuildLatestRows(t *testing.T) {
	body := []byte(`{
		"web":    {"id":"p-web-abc","branch":"main","commitSha":"0123456789abcdef","imageTag":"t1","status":"succeeded","startedAt":"2026-08-17T10:00:00Z"},
		"api":    {"id":"p-api-def","branch":"main","commitSha":"fedcba9876543210","imageTag":"t2","status":"failed","startedAt":"2026-08-17T09:00:00Z"}
	}`)
	rows, ok := buildLatestRows(body)
	if !ok {
		t.Fatal("well-formed map[service]summary payload should be table-renderable")
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Sorted by service so output is stable.
	if rows[0][0] != "api" || rows[1][0] != "web" {
		t.Errorf("rows must be keyed+sorted by service, got %q then %q", rows[0][0], rows[1][0])
	}
	// The SHA is clipped like `build list` does.
	joined := strings.Join(rows[1], "|")
	if !strings.Contains(joined, "0123456789ab") || strings.Contains(joined, "0123456789abc") {
		t.Errorf("sha should be clipped to 12 chars, row: %q", joined)
	}
	if !strings.Contains(joined, "succeeded") {
		t.Errorf("row should carry the status, got: %q", joined)
	}
}

func TestBuildLatestRows_MalformedFallsBack(t *testing.T) {
	if _, ok := buildLatestRows([]byte(`["not","a","map"]`)); ok {
		t.Error("non-map payload must report ok=false so the caller falls back to JSON")
	}
}

func TestRenderSQLTable(t *testing.T) {
	var buf bytes.Buffer
	data := map[string]any{
		"columns": []any{"id", "email"},
		"rows":    []any{[]any{float64(1), "a@x.com"}, []any{float64(2), "b@x.com"}},
	}
	if !renderSQLTable(&buf, data) {
		t.Fatal("columns+rows shape should render as a table")
	}
	out := buf.String()
	for _, want := range []string{"ID", "EMAIL", "a@x.com", "b@x.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSQLTable_NonTabularFallsBack(t *testing.T) {
	var buf bytes.Buffer
	if renderSQLTable(&buf, map[string]any{"detail": "no rows key"}) {
		t.Error("shape without columns/rows must return false so the caller emits JSON")
	}
	if renderSQLTable(&buf, map[string]any{"rows": []any{}, "columns": []any{}}) {
		t.Error("empty columns must return false (nothing tabular to show)")
	}
}
