package handlers

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/db"
	"kuso/server/internal/projects"
)

type revertRecorder struct {
	ProjectsAPI
	service, environment int
	envImage             []string
}

func (r *revertRecorder) SetEnvironmentImage(_ context.Context, project, service, env, repo, tag string) error {
	r.envImage = append(r.envImage, project+"/"+service+"@"+env+"="+repo+":"+tag)
	return nil
}

func (r *revertRecorder) RevertServiceSnapshot(context.Context, string, string, []byte) error {
	r.service++
	return nil
}

func (r *revertRecorder) RevertEnvironmentSnapshot(context.Context, string, string, []byte) error {
	r.environment++
	return nil
}

func TestReplayRevisionRefusesInformational(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"service", "environment", "cron"} {
		rec := &revertRecorder{}
		h := &ProjectsHandler{Svc: rec}
		_, err := h.replayRevision(context.Background(), &db.Revision{
			Project: "alpha", Kind: kind, Name: "x",
			Snapshot: []byte(`{"op":"whatever","informational":true}`),
		})
		if !errors.Is(err, projects.ErrNotRevertable) {
			t.Errorf("%s: err = %v, want ErrNotRevertable", kind, err)
		}
		if rec.service+rec.environment != 0 {
			t.Errorf("%s: informational revision reached a reverter", kind)
		}
	}
}

func TestReplayRevisionDispatchesByKind(t *testing.T) {
	t.Parallel()
	rec := &revertRecorder{}
	h := &ProjectsHandler{Svc: rec}
	ctx := context.Background()
	if _, err := h.replayRevision(ctx, &db.Revision{Kind: "service", Snapshot: []byte(`{"patch":{"port":1}}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.replayRevision(ctx, &db.Revision{Kind: "environment", Snapshot: []byte(`{"op":"environment.domains"}`)}); err != nil {
		t.Fatal(err)
	}
	if rec.service != 1 || rec.environment != 1 {
		t.Fatalf("service=%d environment=%d, want 1 each", rec.service, rec.environment)
	}
	// A cron row without the informational flag can't come from this
	// server, but must still not be silently accepted.
	if _, err := h.replayRevision(ctx, &db.Revision{Kind: "cron", Snapshot: []byte(`{}`)}); !errors.Is(err, errRevertUnsupported) {
		t.Fatalf("cron err = %v, want errRevertUnsupported", err)
	}
}

func TestRevertImageToEnvScopesToOneEnv(t *testing.T) {
	t.Parallel()
	rec := &revertRecorder{}
	h := &ProjectsHandler{Svc: rec}
	ctx := context.Background()
	err := h.revertImageToEnv(ctx, &db.Revision{Project: "shop", Kind: "service", Name: "web",
		Snapshot: []byte(`{"patch":{"image":{"repository":"nginx","tag":"1.27"}}}`)}, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.envImage) != 1 || rec.envImage[0] != "shop/web@staging=nginx:1.27" {
		t.Fatalf("envImage = %v", rec.envImage)
	}
	if rec.service != 0 {
		t.Fatal("env-scoped rollback replayed the service-wide change")
	}
	// A revision that changed more than the image can't be scoped to one env.
	for _, snap := range []string{`{"patch":{"image":{"repository":"nginx"},"port":80}}`, `{"patch":{"port":80}}`, `{"op":"service.envVars"}`} {
		err := h.revertImageToEnv(ctx, &db.Revision{Project: "shop", Kind: "service", Name: "web", Snapshot: []byte(snap)}, "staging")
		if !errors.Is(err, projects.ErrNotRevertable) {
			t.Errorf("%s: err = %v, want ErrNotRevertable", snap, err)
		}
	}
}
