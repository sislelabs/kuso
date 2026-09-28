package auth

import (
	"context"
	"testing"
)

func TestActorName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		claims *Claims
		want   string
	}{
		{"username wins over opaque id", &Claims{UserID: "3cb3589ec763fa2cfff589acb1616fb3", Username: "ivo"}, "ivo"},
		{"id when no username", &Claims{UserID: "3cb3589ec763fa2cfff589acb1616fb3"}, "3cb3589ec763fa2cfff589acb1616fb3"},
		{"no claims", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.claims != nil {
				ctx = ContextWithClaims(ctx, c.claims)
			}
			if got := ActorName(ctx); got != c.want {
				t.Errorf("ActorName = %q, want %q", got, c.want)
			}
		})
	}
}
