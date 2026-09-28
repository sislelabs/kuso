package handlers

// addon_dial_ns_test.go pins that every server-side dial of an addon
// qualifies the conn Secret's short host with the addon's namespace.
// Live failure: project e2e runs in namespace kuso-e2e, its conn Secret
// says POSTGRES_HOST=e2e-db, kuso-server (namespace kuso) looked that
// up bare and got "no such host" — SQL browser 502, backup download dead.

import (
	"context"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
)

const customNS = "kuso-e2e"

// customNSBackupsHandler wires a project "e2e" whose execution namespace
// is kuso-e2e, with addon CR + conn Secret living there (as they do live).
func customNSBackupsHandler(t *testing.T, kind string, connData map[string]string) *BackupsHandler {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRAddons:   "KusoAddonList",
		kube.GVRProjects: "KusoProjectList",
	})
	seed := func(gvr schema.GroupVersionResource, kindName, ns string, obj any) {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kindName))
		u.SetNamespace(ns)
		if err := dyn.Tracker().Create(gvr, u, ns); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	seed(kube.GVRProjects, "KusoProject", "kuso", &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e"},
		Spec:       kube.KusoProjectSpec{Namespace: customNS},
	})
	seed(kube.GVRAddons, "KusoAddon", customNS, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-db"},
		Spec:       kube.KusoAddonSpec{Project: "e2e", Kind: kind},
	})
	data := map[string][]byte{}
	for k, v := range connData {
		data[k] = []byte(v)
	}
	cs := kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-db-conn", Namespace: customNS},
		Data:       data,
	})
	kc := &kube.Client{Clientset: cs, Dynamic: dyn}
	svc := addons.New(kc, "kuso")
	svc.NSResolver = kube.NewProjectNamespaceResolver(kc, "kuso")
	return &BackupsHandler{Kube: kc, Addons: svc, Namespace: "kuso"}
}

func TestPGTarget_QualifiesHostForCustomNamespace(t *testing.T) {
	t.Parallel()
	h := customNSBackupsHandler(t, "postgres", map[string]string{
		"POSTGRES_HOST":     "e2e-db",
		"POSTGRES_PASSWORD": "pw",
		"POSTGRES_DB":       "e2e",
	})
	tgt, err := h.pgTarget(context.Background(), "e2e", "db", "")
	if err != nil {
		t.Fatalf("pgTarget: %v", err)
	}
	if want := "e2e-db.kuso-e2e.svc"; tgt.host != want {
		t.Fatalf("SQL browser dial host = %q, want %q", tgt.host, want)
	}
}

func TestAddonDSN_QualifiesHostForCustomNamespace(t *testing.T) {
	t.Parallel()
	h := customNSBackupsHandler(t, "postgres", map[string]string{
		"POSTGRES_HOST":     "e2e-db",
		"POSTGRES_PASSWORD": "pw",
		"POSTGRES_DB":       "e2e",
	})
	dsn, err := h.addonDSN(context.Background(), customNS, "e2e-db")
	if err != nil {
		t.Fatalf("addonDSN: %v", err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if want := "e2e-db.kuso-e2e.svc"; u.Hostname() != want {
		t.Fatalf("pg_dump dial host = %q, want %q (dsn %s)", u.Hostname(), want, dsn)
	}
}

func TestClickhouseConnInfo_QualifiesHostForCustomNamespace(t *testing.T) {
	t.Parallel()
	h := customNSBackupsHandler(t, "clickhouse", map[string]string{
		"CLICKHOUSE_HOST":     "e2e-db",
		"CLICKHOUSE_PASSWORD": "pw",
	})
	info, ok, err := h.clickhouseConnInfo(context.Background(), "e2e", "db")
	if err != nil || !ok {
		t.Fatalf("clickhouseConnInfo: ok=%v err=%v", ok, err)
	}
	if want := "http://e2e-db.kuso-e2e.svc:8123"; info.baseURL != want {
		t.Fatalf("clickhouse baseURL = %q, want %q", info.baseURL, want)
	}
}

func TestAddonS3Endpoint_QualifiesHostForCustomNamespace(t *testing.T) {
	t.Parallel()
	h := customNSBackupsHandler(t, "s3", map[string]string{
		"S3_ENDPOINT":          "http://e2e-db-storage:9000",
		"S3_BUCKET":            "b",
		"S3_ACCESS_KEY_ID":     "a",
		"S3_SECRET_ACCESS_KEY": "s",
	})
	cli, _, err := h.addonS3Client(context.Background(), customNS, "e2e-db")
	if err != nil {
		t.Fatalf("addonS3Client: %v", err)
	}
	got := ""
	if ep := cli.Options().BaseEndpoint; ep != nil {
		got = *ep
	}
	if !strings.Contains(got, "e2e-db-storage.kuso-e2e.svc:9000") {
		t.Fatalf("s3 endpoint = %q, want host qualified with %s", got, customNS)
	}
}
