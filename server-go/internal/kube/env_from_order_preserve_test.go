package kube

import (
	"slices"
	"testing"
)

func TestPreserveEnvFromOrder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		existing, desired []string
		want              []string
	}{
		{"same set keeps existing order",
			[]string{"cache", "queue", "svc-secrets", "psdb"},
			[]string{"cache", "psdb", "queue", "svc-secrets"},
			[]string{"cache", "queue", "svc-secrets", "psdb"}},
		{"new names append in desired order",
			[]string{"queue", "svc-secrets"},
			[]string{"cache", "psdb", "queue", "svc-secrets"},
			[]string{"queue", "svc-secrets", "cache", "psdb"}},
		{"dropped names disappear",
			[]string{"cache", "old-db", "svc-secrets"},
			[]string{"svc-secrets", "cache"},
			[]string{"cache", "svc-secrets"}},
		{"fresh env takes desired order",
			nil,
			[]string{"cache", "psdb"},
			[]string{"cache", "psdb"}},
		{"duplicates in existing collapse",
			[]string{"cache", "cache", "psdb"},
			[]string{"psdb", "cache"},
			[]string{"cache", "psdb"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PreserveEnvFromOrder(c.existing, c.desired); !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
