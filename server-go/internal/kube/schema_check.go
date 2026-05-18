package kube

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// SchemaMismatch describes a single field the server expects on a
// CRD that the live cluster's CRD definition doesn't carry.
type SchemaMismatch struct {
	CRD   string // e.g. "kusoprojects.application.kuso.sislelabs.com"
	Field string // dotted path, e.g. "spec.namespace"
}

func (m SchemaMismatch) String() string { return m.CRD + ": missing " + m.Field }

// CheckCRDSchemas walks the registered Kuso CRDs and confirms the
// live cluster has every spec field the server is going to write.
// Missing fields would otherwise be silently pruned by the apiserver
// when this build issues a write, which is exactly the v0.7.x data-
// loss class the audit flagged. On any mismatch readyz fails and
// the server refuses to take traffic; the operator runs
// `kubectl apply -f operator/config/crd/bases/...` to fix.
//
// Pass nil cfg to fall through to in-cluster config. Pass a typed
// list of (typeFor, GVR, plural) tuples — we read the Go field tags
// reflectively to produce the "expected fields" set, and List the
// live CRD's openAPIV3Schema to produce the "actual fields" set.
//
// Returns nil when all CRDs match. Returns a non-nil slice of
// mismatches when at least one field is missing — the operator can
// log each one individually.
func CheckCRDSchemas(ctx context.Context, cfg *rest.Config, kinds []SchemaKind) ([]SchemaMismatch, error) {
	cs, err := apiextclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("apiext client: %w", err)
	}
	var out []SchemaMismatch
	for _, k := range kinds {
		live, err := cs.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, k.CRDName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				out = append(out, SchemaMismatch{CRD: k.CRDName, Field: "(CRD not installed)"})
				continue
			}
			return nil, fmt.Errorf("get CRD %s: %w", k.CRDName, err)
		}
		actual := extractLiveFields(live)
		expected := extractExpectedFields(k.Sample)
		for f := range expected {
			if _, ok := actual[f]; !ok {
				out = append(out, SchemaMismatch{CRD: k.CRDName, Field: f})
			}
		}
	}
	return out, nil
}

// SchemaKind tuples a CRD name with a sample of its typed Go struct.
// We use reflection on Sample to enumerate the spec field paths the
// server will write.
type SchemaKind struct {
	CRDName string
	Sample  any // pass a value, not a pointer (e.g. KusoProject{})
}

// CheckSchemas is the canonical bootstrap check. It uses the Client's
// rest.Config to dial the apiextensions API, then verifies every
// kuso CRD's live schema has every spec field the server expects.
//
// Returns (nil, nil) on a clean check. Returns (mismatches, nil) when
// one or more CRDs are present-but-stale — the server should refuse
// to take traffic until the operator applies the latest CRD YAMLs.
// Returns (nil, err) on RPC failure.
//
// Pass nil for kinds to use the default kuso surface; tests can pass
// a narrower set.
func (c *Client) CheckSchemas(ctx context.Context, kinds []SchemaKind) ([]SchemaMismatch, error) {
	if kinds == nil {
		kinds = DefaultSchemaKinds()
	}
	return CheckCRDSchemas(ctx, c.Config, kinds)
}

// DefaultSchemaKinds is the list of kuso CRDs the schema check
// walks at startup. Add new CRDs here as they're introduced.
func DefaultSchemaKinds() []SchemaKind {
	return []SchemaKind{
		{CRDName: "kusoprojects." + GroupName, Sample: KusoProject{}},
		{CRDName: "kusoservices." + GroupName, Sample: KusoService{}},
		{CRDName: "kusoenvironments." + GroupName, Sample: KusoEnvironment{}},
		{CRDName: "kusoaddons." + GroupName, Sample: KusoAddon{}},
		{CRDName: "kusobuilds." + GroupName, Sample: KusoBuild{}},
		{CRDName: "kusocrons." + GroupName, Sample: KusoCron{}},
	}
}

// extractLiveFields walks the served openAPIV3Schema and returns
// every field path under spec.* as a flat set ("spec.foo",
// "spec.foo.bar"). We deliberately don't compare types — a field
// renamed from string to []string would still pass, but the data-
// loss case we care about is "field went away entirely."
func extractLiveFields(crd *apiextv1.CustomResourceDefinition) map[string]struct{} {
	out := map[string]struct{}{}
	for _, v := range crd.Spec.Versions {
		if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		walkSchema(v.Schema.OpenAPIV3Schema, "", out)
	}
	return out
}

func walkSchema(p *apiextv1.JSONSchemaProps, prefix string, out map[string]struct{}) {
	if p == nil {
		return
	}
	for name, sub := range p.Properties {
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		out[path] = struct{}{}
		walkSchema(&sub, path, out)
	}
}

// extractExpectedFields enumerates the JSON field paths under spec
// in the Go type via reflection. We only walk one level of nesting
// — the live CRD's schema for nested structs is generated by
// controller-gen and is more permissive than the Go struct (preserves
// unknown fields), so a deep walk gives lots of false positives.
// Top-level spec coverage is what catches the "field removed from
// CRD" case.
func extractExpectedFields(sample any) map[string]struct{} {
	out := map[string]struct{}{}
	rv := reflect.ValueOf(sample)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	t := rv.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Name != "Spec" {
			continue
		}
		walkStruct(f.Type, "spec", out)
		break
	}
	return out
}

func walkStruct(t reflect.Type, prefix string, out map[string]struct{}) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		path := prefix + "." + tag
		out[path] = struct{}{}
		// Do NOT recurse — see extractExpectedFields comment.
	}
}
