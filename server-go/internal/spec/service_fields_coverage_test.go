package spec

import (
	"reflect"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// fillNonZero sets every reachable field of v to a non-zero value.
func fillNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillNonZero(p.Elem())
		v.Set(p)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillNonZero(v.Field(i))
			}
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillNonZero(s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fillNonZero(k)
		fillNonZero(e)
		m.SetMapIndex(k, e)
		v.Set(m)
	}
}

// Every kuso.yaml service field must change the create request and the
// update patch, or be listed here with the reason it doesn't. A new
// ServiceSpec field fails this test until it's wired or classified: the
// hand-maintained-mapping class that dropped branch/internal/placement/
// volumes on create.
func TestServiceSpecFieldsReachCreateAndPatch(t *testing.T) {
	createExcluded := map[string]string{
		"Name": "identity, not a setting",
	}
	patchExcluded := map[string]string{
		"Name": "identity, not a setting",
		"Env":  "applied by SetEnv, not PatchService",
		"Size": "create-only: resources are edited on the live service",
	}
	base := ServiceSpec{Name: "api", Repo: "https://github.com/o/r"}
	baseCreate, basePatch := serviceCreateReq(base), servicePatchReq(base)
	typ := reflect.TypeOf(ServiceSpec{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		spec := base
		if name == "Env" {
			// A fully-filled EnvValue is a generate directive, which apply
			// handles outside the create request; use a plain literal.
			spec.Env = map[string]EnvValue{"K": {Value: "v"}}
		} else {
			fillNonZero(reflect.ValueOf(&spec).Elem().Field(i))
		}
		if _, skip := createExcluded[name]; !skip && reflect.DeepEqual(serviceCreateReq(spec), baseCreate) {
			t.Errorf("ServiceSpec.%s does not reach serviceCreateReq; map it or add it to createExcluded with a reason", name)
		}
		if _, skip := patchExcluded[name]; !skip && reflect.DeepEqual(servicePatchReq(spec), basePatch) {
			t.Errorf("ServiceSpec.%s does not reach servicePatchReq; map it or add it to patchExcluded with a reason", name)
		}
	}
}

func TestServiceCreateReq_CarriesBornWithFields(t *testing.T) {
	req := serviceCreateReq(ServiceSpec{
		Name: "api", Repo: "https://github.com/o/r", Branch: "release", Path: "apps/api",
		Internal: true, PrivateEgress: true, PlatformAPIEgress: true, WaitForCI: true,
		Placement: &PlacementSpec{Labels: map[string]string{"region": "eu"}},
		Volumes:   []VolumeSpec{{Name: "data", MountPath: "/data", SizeGi: 5}},
		Domains:   []DomainSpec{{Host: "*.shop.example.com", TLS: true, TLSSecret: "wild"}},
	})
	if req.Repo == nil || req.Repo.DefaultBranch != "release" || req.Repo.Path != "apps/api" {
		t.Errorf("repo = %+v, want branch release + path apps/api", req.Repo)
	}
	if !req.Internal || !req.PrivateEgress || !req.PlatformAPIEgress || !req.WaitForCI {
		t.Errorf("network/CI flags dropped: %+v", req)
	}
	if req.Placement == nil || req.Placement.Labels["region"] != "eu" {
		t.Errorf("placement = %+v", req.Placement)
	}
	if len(req.Volumes) != 1 || req.Volumes[0].MountPath != "/data" {
		t.Errorf("volumes = %+v", req.Volumes)
	}
	if len(req.Domains) != 1 || req.Domains[0].TLSSecret != "wild" {
		t.Errorf("wildcard domain lost tlsSecret: %+v", req.Domains)
	}
}

// The CRD stores every domain as tls:true; a file omitting tls (false)
// must not plan a domains change on every apply.
func TestServiceDiff_DomainTLSBitIsNotDrift(t *testing.T) {
	live := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-api"},
		Spec:       kube.KusoServiceSpec{Project: "shop", Domains: []kube.KusoDomain{{Host: "api.shop.example.com", TLS: true}}},
	}
	desired := exportService("shop", *live)
	desired.Domains[0].TLS = false
	req, fields := diffServiceSpec(live, desired)
	if req.Domains != nil || len(fields) != 0 {
		t.Fatalf("tls bit planned a change: domains=%v fields=%+v", req.Domains, fields)
	}
}

func TestServiceDiff_RepoBranchPath(t *testing.T) {
	live := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-api"},
		Spec: kube.KusoServiceSpec{
			Project: "shop",
			Repo:    &kube.KusoRepoRef{URL: "https://gitlab.com/o/r.git", DefaultBranch: "main", Path: ".", Provider: "gitlab", TokenSecret: "shop-api-repo-token"},
		},
	}
	desired := exportService("shop", *live)
	if req, fields := diffServiceSpec(live, desired); req.Repo != nil || len(fields) != 0 {
		t.Fatalf("unchanged repo planned a change: repo=%+v fields=%+v", req.Repo, fields)
	}

	desired.Branch, desired.Path = "release", "apps/api"
	req, fields := diffServiceSpec(live, desired)
	var got []string
	for _, f := range fields {
		got = append(got, f.Field)
	}
	if !slices.Contains(got, "branch") || !slices.Contains(got, "path") {
		t.Fatalf("fields = %v, want branch + path", got)
	}
	if req.Repo == nil || req.Repo.Branch != "release" || req.Repo.Path != "apps/api" || req.Repo.Provider != "gitlab" {
		t.Fatalf("repo patch = %+v", req.Repo)
	}

	desired = exportService("shop", *live)
	desired.Repo = "https://gitlab.com/o/other.git"
	if _, fields := diffServiceSpec(live, desired); len(fields) != 1 || fields[0].Field != "repo" {
		t.Fatalf("repo URL change fields = %+v", fields)
	}
}
