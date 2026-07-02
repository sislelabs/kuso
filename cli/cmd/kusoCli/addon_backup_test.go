package kusoCli

import "testing"

func TestResolveRestoreConfirm(t *testing.T) {
	cases := []struct {
		name    string
		addon   string
		into    string
		confirm string
		wantVal string
		wantErr bool
	}{
		{"in-place unconfirmed rejected", "postgres", "", "", "", true},
		{"in-place wrong confirm rejected", "postgres", "", "pg", "", true},
		{"in-place confirmed ok", "postgres", "", "postgres", "postgres", false},
		{"into-self unconfirmed rejected", "postgres", "postgres", "", "", true},
		{"into-self confirmed ok", "postgres", "postgres", "postgres", "postgres", false},
		{"into sibling no confirm needed", "postgres", "postgres-rehearse", "", "", false},
		{"into sibling passes confirm through", "postgres", "postgres-rehearse", "x", "x", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveRestoreConfirm(c.addon, c.into, c.confirm)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.wantVal {
				t.Errorf("val = %q, want %q", got, c.wantVal)
			}
		})
	}
}
