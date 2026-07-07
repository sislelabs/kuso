package kusoCli

import "testing"

func TestValidateAddonTLSFlag(t *testing.T) {
	cases := []struct {
		val     string
		wantErr bool
	}{
		{"disable", false},
		{"require", false},
		{"", true},           // --tls with empty value is a mistake, not "leave alone"
		{"verify-full", true}, // not supported by the chart
		{"on", true},
	}
	for _, c := range cases {
		if err := validateAddonTLSFlag(c.val); (err != nil) != c.wantErr {
			t.Errorf("validateAddonTLSFlag(%q) err = %v, wantErr %v", c.val, err, c.wantErr)
		}
	}
}
