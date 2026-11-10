package builds

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// BLOB-1: once the retention sweep deleted a build's CR (24h), the next
// webhook build of the same commit for ANOTHER branch picked the plain
// sha12 tag again and pushed over the image production was running.
func TestCreate_StagingBuildAfterCRSweepNeverReusesProductionTag(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	reg := &fakeDeleter{digests: map[string]string{}}
	s.Images = reg
	ctx := context.Background()

	prod, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref, Branch: "main", TriggeredBy: "webhook"})
	if err != nil {
		t.Fatalf("production build: %v", err)
	}
	if prod.Spec.Image == nil || prod.Spec.Image.Tag != "abcdef012345" {
		t.Fatalf("fresh production build should keep the plain sha tag, got %+v", prod.Spec.Image)
	}
	// The push landed, then the retention sweep deleted the CR.
	reg.digests["alpha/web:abcdef012345"] = "sha256:prod"
	if err := s.Cancel(ctx, "alpha", "web", prod.Name); err != nil {
		t.Fatalf("finish production build: %v", err)
	}
	if err := s.Kube.DeleteKusoBuild(ctx, "kuso", prod.Name); err != nil {
		t.Fatalf("sweep production CR: %v", err)
	}

	staging, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref, Branch: "staging", TriggeredBy: "webhook"})
	if err != nil {
		t.Fatalf("staging build: %v", err)
	}
	if staging.Spec.Image == nil || staging.Spec.Image.Tag == "abcdef012345" {
		t.Fatalf("staging build reused production's tag: %+v", staging.Spec.Image)
	}
	if !strings.HasPrefix(staging.Spec.Image.Tag, "abcdef012345-") {
		t.Errorf("staging tag %q should still start with the short sha", staging.Spec.Image.Tag)
	}
	// A redelivery of the staging push is still deduped by its CR.
	if _, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref, Branch: "staging", TriggeredBy: "webhook"}); !errors.Is(err, ErrConflict) {
		t.Errorf("staging redelivery: want ErrConflict, got %v", err)
	}
}

// Same commit, same branch, after the CR sweep: the build env may have
// changed since, so it must not push over the tag production runs either.
func TestCreate_ProductionRebuildAfterCRSweepGetsFreshTag(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	s.Images = &fakeDeleter{digests: map[string]string{"alpha/web:abcdef012345": "sha256:prod"}}
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: ref, Branch: "main", TriggeredBy: "webhook"})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got.Spec.Image == nil || got.Spec.Image.Tag == "abcdef012345" {
		t.Fatalf("rebuild pushed over the existing registry tag: %+v", got.Spec.Image)
	}
}

// An unreachable registry must not fall back to the plain tag.
func TestCreate_RegistryProbeFailureUsesUniqueTag(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	s.Images = erroringProbe{}
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: ref, Branch: "main", TriggeredBy: "webhook"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got.Spec.Image == nil || got.Spec.Image.Tag == "abcdef012345" {
		t.Fatalf("probe failure fell back to the plain tag: %+v", got.Spec.Image)
	}
}

// Previews and non-default branches are identity-qualified even when no
// other build of the commit exists, so they can never land on the tag a
// later production build of that commit will use.
func TestCreate_NonProductionWebhookBuildIsAlwaysQualified(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	for _, req := range []CreateBuildRequest{
		{Ref: ref, Branch: "staging", TriggeredBy: "webhook"},
		{Ref: ref, Branch: "main", TriggeredBy: "webhook", PreviewEnv: "alpha-web-pr-7"},
	} {
		s := fakeService(t,
			seedProject("alpha", "main", "https://github.com/example/alpha", 0),
			seedService("alpha", "web"),
		)
		got, err := s.Create(context.Background(), "alpha", "web", req)
		if err != nil {
			t.Fatalf("%+v: %v", req, err)
		}
		if got.Spec.Image == nil || got.Spec.Image.Tag == "abcdef012345" {
			t.Errorf("%+v got the production tag %+v", req, got.Spec.Image)
		}
	}
}

type erroringProbe struct{}

func (erroringProbe) DeleteImageTag(context.Context, string, string) error { return nil }
func (erroringProbe) ResolveTagDigest(context.Context, string, string) (string, error) {
	return "", errors.New("registry unreachable")
}
