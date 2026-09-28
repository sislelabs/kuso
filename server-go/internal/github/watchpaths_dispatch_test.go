package github

import (
	"context"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedMonorepoSvc(project, service, path string, watch []string) seed {
	s := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service,
			Namespace: "kuso",
			Labels:    map[string]string{"kuso.sislelabs.com/project": project, "kuso.sislelabs.com/service": service},
		},
		Spec: kube.KusoServiceSpec{
			Project:    project,
			Repo:       &kube.KusoRepoRef{URL: "https://github.com/example/mono", Path: path},
			Port:       3000,
			WatchPaths: watch,
		},
	}
	return typedSeed(kube.GVRServices, "KusoService", s)
}

func monorepoPush(message string, commitsJSON string) []byte {
	return []byte(fmt.Sprintf(`{
		"ref": "refs/heads/main",
		"after": "0123456789abcdef0123456789abcdef01234567",
		"repository": {"full_name": "example/mono", "default_branch": "main"},
		"head_commit": {"id": "0123456789abcdef0123456789abcdef01234567", "message": %q},
		"commits": %s
	}`, message, commitsJSON))
}

func buildCounts(t *testing.T, d *Dispatcher, project string, services ...string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, s := range services {
		bs, err := d.Builds.List(context.Background(), project, s)
		if err != nil {
			t.Fatalf("list builds %s: %v", s, err)
		}
		out[s] = len(bs)
	}
	return out
}

func newMonorepoDispatcher(t *testing.T) *Dispatcher {
	return newDispatcher(t,
		seedProj("mono", "https://github.com/example/mono", "main", false, 0),
		seedMonorepoSvc("mono", "web", "apps/web", []string{"apps/web/**"}),
		seedMonorepoSvc("mono", "api", "apps/api", nil),
		seedMonorepoSvc("mono", "docs", "apps/docs", []string{"apps/docs/**", "packages/ui/**"}),
		seedMonorepoSvc("mono", "root", ".", nil),
	)
}

func TestDispatch_PushBuildsOnlyServicesWhoseWatchPathsMatch(t *testing.T) {
	t.Parallel()
	d := newMonorepoDispatcher(t)
	body := monorepoPush("feat(api): add route", `[
		{"added":["apps/api/routes.go"],"modified":["packages/ui/button.tsx"],"removed":[]}
	]`)
	if err := d.Dispatch(context.Background(), "push", body); err != nil {
		t.Fatal(err)
	}
	got := buildCounts(t, d, "mono", "web", "api", "docs", "root")
	want := map[string]int{"web": 0, "api": 1, "docs": 1, "root": 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("builds = %v, want %v", got, want)
	}
}

func TestDispatch_PushWithUnknownFilesBuildsEverything(t *testing.T) {
	t.Parallel()
	d := newMonorepoDispatcher(t)
	commits := "[" + strings.TrimSuffix(strings.Repeat(`{"modified":["apps/api/x.go"]},`, pushCommitCap), ",") + "]"
	if err := d.Dispatch(context.Background(), "push", monorepoPush("big merge", commits)); err != nil {
		t.Fatal(err)
	}
	got := buildCounts(t, d, "mono", "web", "api", "docs", "root")
	for s, n := range got {
		if n != 1 {
			t.Errorf("possibly-truncated push must build %s anyway, got %d builds", s, n)
		}
	}
}

func TestDispatch_PushWithSkipCIBuildsNothing(t *testing.T) {
	t.Parallel()
	d := newMonorepoDispatcher(t)
	body := monorepoPush("docs: typo [skip ci]", `[{"modified":["apps/api/x.go"]}]`)
	if err := d.Dispatch(context.Background(), "push", body); err != nil {
		t.Fatal(err)
	}
	got := buildCounts(t, d, "mono", "web", "api", "docs", "root")
	for s, n := range got {
		if n != 0 {
			t.Errorf("[skip ci] push built %s (%d builds)", s, n)
		}
	}
}
