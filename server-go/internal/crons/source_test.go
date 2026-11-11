package crons

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

func TestSourceCandidates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cmd  []string
		want []string
	}{
		{[]string{"node_modules/.bin/tsx", "src/cron/daily.ts"}, []string{"src/cron/daily.ts"}},
		{[]string{"sh", "-c", "cd app && python ./jobs/sweep.py --dry-run"}, []string{"jobs/sweep.py"}},
		{[]string{"node", "--env-file=.env", "dist/cron.js", "dist/cron.js"}, []string{"dist/cron.js"}},
		{[]string{"npm", "run", "sweep"}, nil},
		{[]string{"/usr/bin/env", "bash", "/app/run.sh", "../x.sh"}, nil},
	}
	for _, c := range cases {
		if got := sourceCandidates(c.cmd); !reflect.DeepEqual(got, c.want) {
			t.Errorf("sourceCandidates(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

type fakeRepoFiles struct {
	files map[string]string // "<ref>:<path>"
	asked []string
}

func (f *fakeRepoFiles) GetRepoFile(_ context.Context, _ int64, _, ref, p string) ([]byte, bool, error) {
	f.asked = append(f.asked, ref+":"+p)
	body, ok := f.files[ref+":"+p]
	return []byte(body), ok, nil
}

func (f *fakeRepoFiles) BlobURL(repoURL, ref, p string) string {
	return repoURL + "/blob/" + ref + "/" + p
}

const (
	liveSHA  = "3fc524991fbc0487274e057d526791347c33f56f"
	newerSHA = "aaaaaaaaaaaa0487274e057d526791347c33f56f"
)

func seedBuild(t *testing.T, dyn *dynamicfake.FakeDynamicClient, name, sha string) {
	t.Helper()
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "kuso",
			Labels:      map[string]string{"kuso.sislelabs.com/project": "p"},
			Annotations: map[string]string{"kuso.sislelabs.com/image-tag": sha[:12]},
		},
		Spec: kube.KusoBuildSpec{
			Project: "p", Service: "p-api", Ref: sha,
			Repo:                 &kube.KusoRepoRef{URL: "https://github.com/o/r", Path: "apps/api"},
			GithubInstallationID: 7,
		},
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(b)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRBuilds.GroupVersion().WithKind("KusoBuild"))
	if err := dyn.Tracker().Create(kube.GVRBuilds, u, "kuso"); err != nil {
		t.Fatal(err)
	}
}

func TestSource_ReadsFileAtTheCommitTheImageWasBuiltFrom(t *testing.T) {
	t.Parallel()
	cron := &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: "p-api-daily", Namespace: "kuso", Labels: map[string]string{"kuso.sislelabs.com/project": "p"}},
		Spec: kube.KusoCronSpec{
			Project: "p", Service: "p-api", Schedule: "0 1 * * *",
			Command: []string{"node_modules/.bin/tsx", "src/cron/daily.ts"},
			Image:   &kube.KusoImage{Repository: "reg/p/api", Tag: liveSHA[:12]},
		},
	}
	svc := cronFakeService(t, cron)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRCrons:  "KusoCronList",
		kube.GVRBuilds: "KusoBuildList",
	})
	m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(cron)
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRCrons.GroupVersion().WithKind("KusoCron"))
	if err := dyn.Tracker().Create(kube.GVRCrons, u, "kuso"); err != nil {
		t.Fatal(err)
	}
	seedBuild(t, dyn, "p-api-a-newer", newerSHA) // undeployed push
	seedBuild(t, dyn, "p-api-live", liveSHA)
	svc.Kube = &kube.Client{Dynamic: dyn}

	files := &fakeRepoFiles{files: map[string]string{
		liveSHA + ":apps/api/src/cron/daily.ts":  "live code",
		newerSHA + ":apps/api/src/cron/daily.ts": "undeployed code",
	}}
	got, err := svc.Source(context.Background(), "p", "api", "daily", files)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.Content != "live code" || got.Commit != liveSHA || got.Path != "apps/api/src/cron/daily.ts" {
		t.Fatalf("got %+v, asked %q", got, files.asked)
	}
}
