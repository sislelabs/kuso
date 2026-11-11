package kusoCli

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestRenderAPIResponse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		include bool
		status  int
		header  http.Header
		// exact, when set, is the whole expected output; otherwise every
		// contains substring must appear.
		exact    string
		contains []string
	}{
		// Pretty-printed, indented output.
		{name: "PrettyJSON", body: `{"b":2,"a":1}`, status: 200, contains: []string{"\n  "}},
		{name: "RawNonJSON", body: "plain text", status: 200, exact: "plain text\n"},
		{name: "Include", body: `{}`, include: true, status: 201,
			header:   http.Header{"Content-Type": []string{"application/json"}},
			contains: []string{"HTTP 201", "Content-Type: application/json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := renderAPIResponse(&buf, []byte(tc.body), tc.include, tc.status, tc.header); err != nil {
				t.Fatal(err)
			}
			if tc.exact != "" && buf.String() != tc.exact {
				t.Fatalf("output = %q, want %q", buf.String(), tc.exact)
			}
			for _, w := range tc.contains {
				if !strings.Contains(buf.String(), w) {
					t.Fatalf("output missing %q:\n%s", w, buf.String())
				}
			}
		})
	}
}
