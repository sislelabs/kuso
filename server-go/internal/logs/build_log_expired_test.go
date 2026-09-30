package logs

import (
	"context"
	"strings"
	"testing"
)

type fakeBuildHistory map[string]string // build name -> project

func (f fakeBuildHistory) GetBuildImage(_ context.Context, project, buildName string) (string, string, string, bool, error) {
	return "", "", "", f[buildName] == project, nil
}

// A build still in the history whose archived log was pruned used to
// report "build:… not found in project …", which reads as a bug. It is an
// expired log; say so. A build unknown to this project still reads as not
// found, so the message can't be used to probe another project's builds.
func TestBuildLogExpiredNotice(t *testing.T) {
	s := &Service{BuildHistory: fakeBuildHistory{"tickero-api-abc": "tickero"}, BuildLogRetentionDays: 30}
	ctx := context.Background()

	msg, ok := s.BuildLogExpired(ctx, "tickero", "build:tickero-api-abc")
	if !ok || !strings.Contains(msg, "30 days") {
		t.Errorf("known build: got (%q, %v), want an expiry notice naming 30 days", msg, ok)
	}
	if _, ok := s.BuildLogExpired(ctx, "other", "build:tickero-api-abc"); ok {
		t.Error("a build from another project must not be reported as expired")
	}
	if _, ok := s.BuildLogExpired(ctx, "tickero", "build:tickero-api-missing"); ok {
		t.Error("a build absent from history must not be reported as expired")
	}
	if _, ok := s.BuildLogExpired(ctx, "tickero", "production"); ok {
		t.Error("a non-build stream must not be reported as expired")
	}
}
