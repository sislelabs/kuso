package kusoCli

import (
	"bytes"
	"net/http"
	"testing"
)

func TestRenderAPIResponse_PrettyJSON(t *testing.T) {
	var buf bytes.Buffer
	err := renderAPIResponse(&buf, []byte(`{"b":2,"a":1}`), false, 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Pretty-printed, indented output.
	if !bytes.Contains(buf.Bytes(), []byte("\n  ")) {
		t.Fatalf("expected indented JSON, got:\n%s", buf.String())
	}
}

func TestRenderAPIResponse_RawNonJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := renderAPIResponse(&buf, []byte("plain text"), false, 200, nil); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "plain text\n" {
		t.Fatalf("raw passthrough wrong: %q", buf.String())
	}
}

func TestRenderAPIResponse_Include(t *testing.T) {
	var buf bytes.Buffer
	h := http.Header{"Content-Type": []string{"application/json"}}
	if err := renderAPIResponse(&buf, []byte(`{}`), true, 201, h); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("HTTP 201")) ||
		!bytes.Contains(buf.Bytes(), []byte("Content-Type: application/json")) {
		t.Fatalf("include header output wrong:\n%s", buf.String())
	}
}
