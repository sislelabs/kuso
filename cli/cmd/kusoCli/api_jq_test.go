package kusoCli

import "testing"

func TestApplyJQ(t *testing.T) {
	for _, tc := range []struct {
		name, body, expr, want string
		wantErr                bool
	}{
		{name: "Field", body: `{"name":"web","port":3000}`, expr: ".name", want: "\"web\"\n"},
		{name: "Iterate", body: `{"items":[{"n":"a"},{"n":"b"}]}`, expr: ".items[].n", want: "\"a\"\n\"b\"\n"},
		{name: "NotJSON", body: "not json", expr: ".x", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := applyJQ([]byte(tc.body), tc.expr)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error for non-JSON body")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Fatalf("jq %s = %q, want %q", tc.expr, out, tc.want)
			}
		})
	}
}
