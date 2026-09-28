package crons

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

type cronRev struct {
	project, kind, name, summary string
	snapshot                     []byte
}

func captureCronRevisions(s *Service) *[]cronRev {
	var out []cronRev
	s.RecordRevision = func(_ context.Context, project, kind, name, summary string, snapshot []byte) {
		out = append(out, cronRev{project, kind, name, summary, snapshot})
	}
	return &out
}

func projectHTTPCron() *kube.KusoCron {
	return &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-ping", Namespace: "kuso", Labels: map[string]string{"kuso.sislelabs.com/project": "alpha"}},
		Spec:       kube.KusoCronSpec{Project: "alpha", Kind: "http", URL: "https://example.com/ping", Schedule: "0 0 * * *"},
	}
}

func serviceCron() *kube.KusoCron {
	return &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-nightly", Namespace: "kuso", Labels: map[string]string{"kuso.sislelabs.com/project": "alpha"}},
		Spec:       kube.KusoCronSpec{Project: "alpha", Service: "alpha-web", Schedule: "0 0 * * *", Command: []string{"echo"}},
	}
}

func TestCronMutationsRecordRevisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := cronFakeService(t, projectHTTPCron(), serviceCron())
	got := captureCronRevisions(s)

	sched := "*/5 * * * *"
	suspend := true
	if _, err := s.Update(ctx, "alpha", "web", "nightly", UpdateCronRequest{Schedule: &sched, Suspend: &suspend}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	hookURL := "https://hooks.example.com/T000/SECRET-hook-path"
	cmd := []string{"curl", "-H", "Authorization: Bearer cron-cmd-token"}
	img := &kube.KusoImage{Repository: "alpine", Tag: "3"}
	if _, err := s.UpdateProject(ctx, "alpha", "ping", UpdateProjectCronRequest{
		Schedule:  &sched,
		Image:     img,
		Command:   cmd,
		OnFailure: &OnFailureUpdate{WebhookURL: hookURL},
	}); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if _, err := s.AddProject(ctx, "alpha", CreateProjectCronRequest{Name: "report", Kind: "http", URL: "https://example.com/r", Schedule: "0 1 * * *"}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if err := s.DeleteProject(ctx, "alpha", "ping"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if err := s.Delete(ctx, "alpha", "web", "nightly"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	want := []cronRev{
		{project: "alpha", kind: "cron", name: "web-nightly", summary: "cron update: schedule, suspend"},
		{project: "alpha", kind: "cron", name: "ping", summary: "cron update: schedule, image, command, onFailure"},
		{project: "alpha", kind: "cron", name: "report", summary: "cron create (http, 0 1 * * *)"},
		{project: "alpha", kind: "cron", name: "ping", summary: "cron delete"},
		{project: "alpha", kind: "cron", name: "web-nightly", summary: "cron delete"},
	}
	if len(*got) != len(want) {
		t.Fatalf("got %d revisions, want %d: %+v", len(*got), len(want), *got)
	}
	for i, w := range want {
		g := (*got)[i]
		if g.project != w.project || g.kind != w.kind || g.name != w.name || g.summary != w.summary {
			t.Errorf("revision %d = %s/%s/%s %q, want %s/%s/%s %q", i, g.project, g.kind, g.name, g.summary, w.project, w.kind, w.name, w.summary)
		}
		var snap struct {
			Informational bool `json:"informational"`
		}
		if err := json.Unmarshal(g.snapshot, &snap); err != nil || !snap.Informational {
			t.Errorf("revision %d snapshot must be informational JSON, got %s", i, g.snapshot)
		}
		for _, leak := range []string{"SECRET-hook-path", "cron-cmd-token"} {
			if strings.Contains(string(g.snapshot), leak) {
				t.Errorf("revision %d snapshot leaks %q: %s", i, leak, g.snapshot)
			}
		}
	}
}
