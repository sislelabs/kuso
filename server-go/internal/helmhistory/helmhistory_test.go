package helmhistory

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
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

func TestPruneNamespace_KeepsNewestAndDeployed(t *testing.T) {
	var objs []runtime.Object
	// 15 revisions; the deployed one is old (a later upgrade failed and
	// the failed revisions piled up on top of it).
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
	other := &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "kuso", Name: "app-secrets", Labels: map[string]string{"app": "x"}},
	}
	objs = append(objs, other)
	scheme := metafake.NewTestScheme()
	metav1.AddMetaToScheme(scheme)
	meta := metafake.NewSimpleMetadataClient(scheme, objs...)
	p := &Pruner{Meta: meta, Keep: 10, Logger: slog.Default()}

	n, err := p.PruneNamespace(context.Background(), "kuso")
	if err != nil {
		t.Fatal(err)
	}
	// db: newest 10 are v6..v15; of v1..v5, v3 is deployed → delete 4.
	if n != 4 {
		t.Fatalf("deleted %d, want 4", n)
	}
	list, err := meta.Resource(secretsGVR).Namespace("kuso").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, it := range list.Items {
		left = append(left, it.Name)
	}
	sort.Strings(left)
	for _, must := range []string{"sh.helm.release.v1.db.v3", "sh.helm.release.v1.db.v6", "sh.helm.release.v1.db.v15",
		"sh.helm.release.v1.web.v1", "sh.helm.release.v1.web.v2", "app-secrets"} {
		if !contains(left, must) {
			t.Errorf("%s was deleted; left = %v", must, left)
		}
	}
	if contains(left, "sh.helm.release.v1.db.v1") || contains(left, "sh.helm.release.v1.db.v5") {
		t.Errorf("old superseded/failed revisions kept: %v", left)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
