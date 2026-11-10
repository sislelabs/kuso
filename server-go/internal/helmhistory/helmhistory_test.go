package helmhistory

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	metafake "k8s.io/client-go/metadata/fake"
)

func relSecret(release string, v int, status string) runtime.Object {
	return &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kuso",
			Name:      fmt.Sprintf("sh.helm.release.v1.%s.v%d", release, v),
			Labels:    map[string]string{"owner": "helm", "name": release, "version": fmt.Sprint(v), "status": status},
		},
	}
}

func newMeta(objs ...runtime.Object) *metafake.FakeMetadataClient {
	scheme := metafake.NewTestScheme()
	metav1.AddMetaToScheme(scheme)
	return metafake.NewSimpleMetadataClient(scheme, objs...)
}

func remaining(t *testing.T, m *metafake.FakeMetadataClient) []string {
	t.Helper()
	list, err := m.Resource(secretsGVR).Namespace("kuso").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range list.Items {
		out = append(out, it.Name)
	}
	return out
}

// fixture: "db" has 15 revisions, deployed v3 under 12 failed upgrades;
// "web" has 2; "app-secrets" isn't a helm release.
func fixture() []runtime.Object {
	var objs []runtime.Object
	for v := 1; v <= 15; v++ {
		status := "superseded"
		switch {
		case v == 3:
			status = "deployed"
		case v > 3:
			status = "failed"
		}
		objs = append(objs, relSecret("db", v, status))
	}
	objs = append(objs, relSecret("web", 1, "deployed"), relSecret("web", 2, "pending-upgrade"))
	return append(objs, &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "kuso", Name: "app-secrets", Labels: map[string]string{"app": "x"}},
	})
}

func TestPruneNamespace_KeepsNewestAndDeployed(t *testing.T) {
	meta := newMeta(fixture()...)
	p := &Pruner{Meta: meta, Keep: 10, Mode: ModeOn, Logger: slog.Default(), noPacing: true}

	n, err := p.PruneNamespace(context.Background(), "kuso")
	if err != nil {
		t.Fatal(err)
	}
	// db: newest 10 are v6..v15; of v1..v5, v3 is deployed → delete 4.
	if n != 4 {
		t.Fatalf("deleted %d, want 4", n)
	}
	left := remaining(t, meta)
	for _, must := range []string{"sh.helm.release.v1.db.v3", "sh.helm.release.v1.db.v6", "sh.helm.release.v1.db.v15",
		"sh.helm.release.v1.web.v1", "sh.helm.release.v1.web.v2", "app-secrets"} {
		if !slices.Contains(left, must) {
			t.Errorf("%s was deleted; left = %v", must, left)
		}
	}
	if slices.Contains(left, "sh.helm.release.v1.db.v1") || slices.Contains(left, "sh.helm.release.v1.db.v5") {
		t.Errorf("old superseded/failed revisions kept: %v", left)
	}
}

// The default mode reports and deletes nothing.
func TestPruneNamespace_DryRunDeletesNothing(t *testing.T) {
	meta := newMeta(fixture()...)
	p := &Pruner{Meta: meta, Keep: 10, Logger: slog.Default(), noPacing: true} // Mode unset = dryrun

	n, err := p.PruneNamespace(context.Background(), "kuso")
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("dryrun reported %d prunable, want 4", n)
	}
	for _, a := range meta.Actions() {
		if a.GetVerb() == "delete" {
			t.Fatalf("dryrun issued a delete: %v", a)
		}
	}
	if got := len(remaining(t, meta)); got != 18 {
		t.Errorf("%d secrets left, want all 18", got)
	}
	if ParseMode("") != ModeDryRun || ParseMode("yes") != ModeDryRun || ParseMode("ON") != ModeOn {
		t.Error("ParseMode must default to dryrun and only enable deletion on an explicit on")
	}
}

// A release with no deployed revision (a first install that never
// succeeded, or an unexpected status value) is left whole: helm-operator
// would treat losing its history as "not installed" and reinstall.
func TestPrunable_SkipsReleaseWithoutDeployed(t *testing.T) {
	var revs []revision
	for v := 1; v <= 20; v++ {
		revs = append(revs, revision{name: fmt.Sprint(v), version: v, status: "failed"})
	}
	if got := prunable(revs, 3); len(got) != 0 {
		t.Fatalf("pruned %d revisions of a release with no deployed revision", len(got))
	}
}

// The highest revision survives even when it is old by status and keep is
// as small as it gets.
func TestPrunable_NeverDeletesHighestRevision(t *testing.T) {
	revs := []revision{
		{name: "v1", version: 1, status: "deployed"},
		{name: "v2", version: 2, status: "superseded"},
		{name: "v9", version: 9, status: "failed"},
	}
	for _, keep := range []int{0, 1} {
		for _, r := range prunable(slices.Clone(revs), keep) {
			if r.version == 9 {
				t.Fatalf("keep=%d: highest revision v9 marked for deletion", keep)
			}
			if r.version == 1 {
				t.Fatalf("keep=%d: deployed revision marked for deletion", keep)
			}
		}
	}
}

func TestCompactRanges(t *testing.T) {
	if got := compactRanges([]int{7, 5, 4, 3, 1}); got != "1,3-5,7" {
		t.Errorf("got %q", got)
	}
}
