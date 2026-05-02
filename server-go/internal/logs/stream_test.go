package logs

import "testing"

// Frame envelope is part of the WS contract; lock its JSON shape so a
// rename in stream.go forces a deliberate update on the frontend.
func TestFrame_JSONShape(t *testing.T) {
	cases := []struct {
		name string
		in   Frame
		want string
	}{
		{
			name: "log",
			in: Frame{
				Type:   "log",
				Pod:    "echo-6c7d-x",
				Stream: "stdout",
				Line:   "hello",
				Ts:     "2026-05-02T12:00:00Z",
			},
			want: `{"type":"log","pod":"echo-6c7d-x","stream":"stdout","line":"hello","ts":"2026-05-02T12:00:00Z"}`,
		},
		{
			name: "ping",
			in:   Frame{Type: "ping"},
			want: `{"type":"ping"}`,
		},
		{
			name: "phase",
			in:   Frame{Type: "phase", Value: "BUILDING"},
			want: `{"type":"phase","value":"BUILDING"}`,
		},
		{
			name: "error",
			in:   Frame{Type: "error", Pod: "p", Message: "kaboom"},
			want: `{"type":"error","pod":"p","message":"kaboom"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := jsonEncode(tc.in)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// jsonEncode is a tiny helper to keep the test free of a json import
// that's already imported in package files; using encoding/json
// directly here is the right call but Go forbids duplicate imports.
func jsonEncode(f Frame) (string, error) {
	b, err := jsonMarshal(f)
	return string(b), err
}
