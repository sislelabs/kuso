package builds

import (
	"context"
	"errors"
	"sort"
	"testing"
)

func TestDeployHook_EnableVerifyRotateDisable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeService(t, seedProject("shop", "main", "https://github.com/acme/api", 0), seedService("shop", "api"))

	if tok, err := s.DeployHookToken(ctx, "shop", "api"); err != nil || tok != "" {
		t.Fatalf("before enable: token=%q err=%v, want none", tok, err)
	}
	if s.VerifyDeployHook(ctx, "shop", "api", "") {
		t.Fatal("an empty token must never verify, enabled or not")
	}

	tok, err := s.EnsureDeployHook(ctx, "shop", "api", false)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(tok) < 32 {
		t.Fatalf("token %q is too short to be a secret", tok)
	}
	again, _ := s.EnsureDeployHook(ctx, "shop", "api", false)
	if again != tok {
		t.Error("enabling twice must return the same token, or the URL the user already pasted breaks")
	}
	if !s.VerifyDeployHook(ctx, "shop", "api", tok) {
		t.Error("the issued token must verify")
	}
	if s.VerifyDeployHook(ctx, "shop", "api", tok+"x") || s.VerifyDeployHook(ctx, "shop", "other", tok) {
		t.Error("a wrong token or another service must not verify")
	}

	rotated, err := s.EnsureDeployHook(ctx, "shop", "api", true)
	if err != nil || rotated == tok {
		t.Fatalf("rotate: token=%q err=%v, want a new token", rotated, err)
	}
	if s.VerifyDeployHook(ctx, "shop", "api", tok) {
		t.Error("the old token must stop working after a rotate")
	}

	if err := s.DeleteDeployHook(ctx, "shop", "api"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if s.VerifyDeployHook(ctx, "shop", "api", rotated) {
		t.Error("no token verifies once the hook is disabled")
	}
}

func TestDeployHook_UnknownServiceIsNotFound(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("shop", "main", "https://github.com/acme/api", 0))
	if _, err := s.EnsureDeployHook(context.Background(), "shop", "ghost", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A push to a branch no environment tracks must not start a build.
func TestDeployedBranches_DefaultPlusEnvBranches(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("shop", "main", "https://github.com/acme/api", 0),
		seedService("shop", "api"),
		seedEnv("shop", "api", "production", ""),
		seedEnv("shop", "api", "staging", "develop"),
	)
	got, err := s.DeployedBranches(context.Background(), "shop", "api")
	if err != nil {
		t.Fatalf("DeployedBranches: %v", err)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "develop" || got[1] != "main" {
		t.Errorf("branches = %v, want [develop main]", got)
	}
}
