package handlers

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

type recreatedProjectSvc struct {
	ProjectsAPI
	created time.Time
}

func (f *recreatedProjectSvc) Get(context.Context, string) (*kube.KusoProject, error) {
	return &kube.KusoProject{ObjectMeta: metav1.ObjectMeta{Name: "acme", CreationTimestamp: metav1.NewTime(f.created)}}, nil
}

// AUTHZ-04: whoever re-creates a deleted project's name must not see (or
// revert to) the previous project's history; instance admins still do.
func TestRevisionVisible_HidesHistoryFromBeforeRecreation(t *testing.T) {
	t.Parallel()
	recreated := time.Now().Add(-time.Hour)
	h := &ProjectsHandler{Svc: &recreatedProjectSvc{created: recreated}}
	old := &db.Revision{Project: "acme", CreatedAt: recreated.Add(-24 * time.Hour)}
	fresh := &db.Revision{Project: "acme", CreatedAt: recreated.Add(time.Minute)}

	member := auth.WithClaimsForTest(context.Background(), &auth.Claims{UserID: "u"})
	admin := auth.WithClaimsForTest(context.Background(), &auth.Claims{UserID: "a", Permissions: []string{string(auth.PermSettingsAdmin)}})

	cases := []struct {
		name string
		ctx  context.Context
		rev  *db.Revision
		want bool
	}{
		{"member, old owner's revision", member, old, false},
		{"member, own revision", member, fresh, true},
		{"admin, old owner's revision", admin, old, true},
	}
	for _, c := range cases {
		got, err := h.revisionVisible(c.ctx, c.rev)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: visible = %v, want %v", c.name, got, c.want)
		}
	}
}
