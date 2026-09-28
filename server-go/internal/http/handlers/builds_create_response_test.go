package handlers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

// A trigger that coalesced into an in-flight build must say so on the
// wire; the CLI used to print "started" for it and hid a wrong build.
func TestCreateBuildResponse_FlagsExisting(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 9, 28, 15, 40, 19, 0, time.UTC)
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-web-46fe4156ba59", CreationTimestamp: metav1.NewTime(created)},
		Spec:       kube.KusoBuildSpec{Service: "e2e-web", Branch: "staging", Ref: "46fe4156ba59e4cd0afd264febce9890068f253d"},
	}

	cases := []struct {
		existing   bool
		wantStatus int
	}{{false, http.StatusCreated}, {true, http.StatusOK}}
	for _, c := range cases {
		status, body := createBuildResponse(builds.CreateOutcome{Build: b, Existing: c.existing})
		if status != c.wantStatus {
			t.Errorf("existing=%v: status %d, want %d", c.existing, status, c.wantStatus)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			ID        string `json:"id"`
			Branch    string `json:"branch"`
			Existing  bool   `json:"existing"`
			CreatedAt string `json:"createdAt"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.ID != b.Name || got.Branch != "staging" || got.Existing != c.existing || got.CreatedAt != "2026-09-28T15:40:19Z" {
			t.Errorf("existing=%v: body %s", c.existing, raw)
		}
	}
}
