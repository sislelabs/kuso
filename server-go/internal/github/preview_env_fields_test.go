package github

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"kuso/server/internal/kube"
)

// previewInheritedFields are the service-derived KusoEnvironmentSpec
// fields a PREVIEW env must copy from its parent KusoService.
//
// The preview literal in dispatcher.go was the fourth env-minting literal
// and the only one NOT covered by internal/projects'
// TestEnvLiteralsShareServiceDerivedFields (that guard lives in a
// different package). It silently omitted ten of these. The chart renders
// each off the env CR alone, so a preview ran with different config from
// the service that spawned it — most seriously PrivateEgress, whose
// absence trips the chart's `if not .Values.privateEgress` branch and
// grants a preview public internet egress that production denies.
var previewInheritedFields = []string{
	"Internal",
	"PrivateEgress",
	"PlatformAPIEgress",
	"Resources",
	"Volumes",
	"Runtime",
	"Command",
	"SecurityContext",
	"Healthcheck",
	"PublicEnv",
	"Release",
	"SnapshotBeforeDeploy",
}

// previewExcludedFields are service-derived fields a preview must NOT
// inherit, with the reason. Listed explicitly so the completeness check
// below can assert that every service-derived field is either inherited
// or deliberately excluded — never merely forgotten.
var previewExcludedFields = map[string]string{
	"Stopped":   "a hard-stopped service must not force new PR previews to boot dead",
	"Sleep":     "previews carry their own scale-down lifecycle",
	"Placement": "previews are scheduled independently of the parent service",
}

// TestPreviewEnvLiteralCoversServiceDerivedFields parses dispatcher.go and
// asserts the preview KusoEnvironmentSpec literal assigns every field in
// previewInheritedFields.
//
// This is deliberately a SOURCE parse rather than a behavioural check: the
// bug class is "a field exists on the spec but nobody stamped it", which
// is invisible to a test that only inspects the constructed object for
// fields it already knows to look at.
func TestPreviewEnvLiteralCoversServiceDerivedFields(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "dispatcher.go", nil, 0)
	if err != nil {
		t.Fatalf("parse dispatcher.go: %v", err)
	}

	// Find the KusoEnvironmentSpec composite literal that carries
	// Kind: "preview" — that's the one minted for a PR.
	var assigned map[string]bool
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "KusoEnvironmentSpec" {
			return true
		}
		got := map[string]bool{}
		isPreview := false
		for _, e := range cl.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			id, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			got[id.Name] = true
			if id.Name == "Kind" {
				if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Value == `"preview"` {
					isPreview = true
				}
			}
		}
		if isPreview {
			assigned = got
			return false
		}
		return true
	})
	if assigned == nil {
		t.Fatal(`could not find the preview KusoEnvironmentSpec literal (Kind: "preview") in dispatcher.go`)
	}

	for _, field := range previewInheritedFields {
		if !assigned[field] {
			t.Errorf("preview env literal does not set %q — a preview will silently run without it "+
				"(the chart reads this field off the env CR alone, and PR resync re-erases anything "+
				"propagate.go restores)", field)
		}
	}
}

// TestPreviewFieldListsCoverServiceSpec is the structural half: every
// field that exists on BOTH KusoServiceSpec and KusoEnvironmentSpec must
// be classified as either inherited or deliberately excluded.
//
// Without this, adding a new service-derived field to the CRD leaves the
// preview path silently behind — exactly how the original ten went
// missing. The sibling guard in internal/projects iterates a
// hand-maintained slice and has the same blind spot; this one reflects
// over the struct so a new field fails the build instead.
func TestPreviewFieldListsCoverServiceSpec(t *testing.T) {
	svcT := reflect.TypeOf(kube.KusoServiceSpec{})
	envT := reflect.TypeOf(kube.KusoEnvironmentSpec{})

	envFields := map[string]bool{}
	for i := 0; i < envT.NumField(); i++ {
		envFields[envT.Field(i).Name] = true
	}
	classified := map[string]bool{}
	for _, n := range previewInheritedFields {
		classified[n] = true
	}
	for n := range previewExcludedFields {
		classified[n] = true
	}

	// Fields that are env-scoped, computed, or resolved per-env rather
	// than copied verbatim off the service. Mirrors the exclusion list
	// documented on internal/projects' serviceDerivedEnvSpecFields.
	notServiceDerived := map[string]bool{
		"Project": true, "Service": true, "Kind": true, "Branch": true,
		"Port": true, "Host": true, "ReplicaCount": true, "Autoscaling": true,
		"SpreadPolicy": true, "AdditionalHosts": true, "TLSHosts": true,
		"WildcardDomains": true, "PullRequest": true, "TTL": true,
		"EnvOverrides": true, "SecretsRev": true, "EnvVars": true,
		"EnvFromSecrets": true, "SharedEnvKeys": true, "SubscribedAddons": true,
		"TLSEnabled": true, "ClusterIssuer": true, "IngressClassName": true,
		"Image": true, "Repo": true, "Previews": true, "Domains": true,
	}

	for i := 0; i < svcT.NumField(); i++ {
		name := svcT.Field(i).Name
		if !envFields[name] || notServiceDerived[name] || classified[name] {
			continue
		}
		t.Errorf("KusoServiceSpec.%s also exists on KusoEnvironmentSpec but is neither inherited "+
			"by the preview env literal nor listed in previewExcludedFields. Add it to the literal "+
			"in dispatcher.go and to previewInheritedFields, or document why a preview must not "+
			"inherit it in previewExcludedFields.", name)
	}
}
