package kube

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// Golden-file round-trip tests for the six CRD types.
//
// Why this exists:
//
// We hand-roll the typed structs in types.go (per docs/REWRITE.md §3 —
// no codegen until we hit pain). That means every time someone adds a
// new spec field, they have to remember to:
//
//   1. Add the Go field with the matching json tag.
//   2. Update the CRD yaml in operator/config/crd/bases/.
//
// Forgetting (1) means a user's CR survives the API server but
// silently loses the new field every time the kuso server reads-then-
// writes it (e.g. on placement edits, on patch operations, on the
// builder spawn path). It's the kind of bug that bites quietly: the
// CR looks fine on disk, the operator reconciles a stale spec.
//
// These tests catch that by feeding a "real-shape" CR JSON through
// the typed struct round-trip and asserting no leaf is dropped.
//
// They are NOT a CRD-schema validator. The CRD yaml is the authority
// for what kube-apiserver accepts; this test is the authority for
// what the kuso server can read+write without losing data.

func TestCRD_RoundTrip_NoFieldLoss(t *testing.T) {
	t.Parallel()

	// Each case picks a typed target and a fixture file. The decoder
	// is generic — pass a pointer to an empty value and the tableau
	// runs the same shape for every kind.
	cases := []struct {
		name    string
		fixture string
		decode  func([]byte) (any, error)
		// ignore lists json paths that are intentionally not preserved on
		// round-trip (server-managed fields, fields the kuso client
		// deliberately drops because the operator owns them, etc).
		// Path syntax: dotted ("metadata.creationTimestamp") with [*]
		// for any slice index.
		ignore []string
	}{
		{
			name:    "KusoProject",
			fixture: "testdata/kusoproject_v07.json",
			decode:  decodeAs[KusoProject],
		},
		{
			name:    "KusoService",
			fixture: "testdata/kusoservice_v07.json",
			decode:  decodeAs[KusoService],
		},
		{
			name:    "KusoEnvironment",
			fixture: "testdata/kusoenvironment_v07.json",
			decode:  decodeAs[KusoEnvironment],
		},
		{
			name:    "KusoAddon",
			fixture: "testdata/kusoaddon_v07.json",
			decode:  decodeAs[KusoAddon],
		},
		{
			name:    "KusoBuild",
			fixture: "testdata/kusobuild_v07.json",
			decode:  decodeAs[KusoBuild],
		},
		{
			name:    "KusoCron",
			fixture: "testdata/kusocron_v08.json",
			decode:  decodeAs[KusoCron],
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(filepath.Clean(tc.fixture))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			// Original shape — what the API server gave us.
			var original map[string]any
			if err := json.Unmarshal(raw, &original); err != nil {
				t.Fatalf("unmarshal original: %v", err)
			}

			// Decode through the typed struct.
			typed, err := tc.decode(raw)
			if err != nil {
				t.Fatalf("decode through typed struct: %v", err)
			}

			// Re-encode the typed struct back to the unstructured map
			// shape — same path the dynamic client takes on Update().
			roundTripped, err := runtime.DefaultUnstructuredConverter.ToUnstructured(typed)
			if err != nil {
				t.Fatalf("to unstructured: %v", err)
			}

			// Now diff. Any leaf in `original` that is missing or
			// different in `roundTripped` is a regression. We allow
			// new fields in the typed struct (they'd appear in
			// roundTripped but not original) — those are forward-
			// compatible additions, not data loss.
			missing := diffLeaves("", original, roundTripped, asSet(tc.ignore))
			if len(missing) > 0 {
				sort.Strings(missing)
				t.Errorf("round-trip lost %d field(s) from %s:\n  %s",
					len(missing), tc.fixture, strings.Join(missing, "\n  "))
			}
		})
	}
}

// TestCRD_RoundTrip_DetectsDataLoss is a meta-test: it proves the
// round-trip checker actually fires when a field is genuinely lost.
// Without this, the main test could silently degrade into a no-op
// (e.g. if isZeroJSONValue grew too permissive) and we wouldn't
// notice.
//
// We construct a typed struct that deliberately omits a non-zero
// field that's present in the JSON, then assert the differ flags it.
func TestCRD_RoundTrip_DetectsDataLoss(t *testing.T) {
	t.Parallel()

	// `Project` is in the JSON; the trimmed struct has no field for
	// it, so the round-trip MUST drop it.
	type trimmedKusoProjectSpec struct {
		BaseDomain string `json:"baseDomain,omitempty"`
		// no `project` field — simulates the "forgot the json tag" bug.
	}
	type trimmedKusoProject struct {
		Spec trimmedKusoProjectSpec `json:"spec,omitempty"`
	}

	raw := []byte(`{
        "spec": {
            "project": "alpha",
            "baseDomain": "alpha.example.com"
        }
    }`)

	var original map[string]any
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var typed trimmedKusoProject
	if err := json.Unmarshal(raw, &typed); err != nil {
		t.Fatalf("decode through trimmed struct: %v", err)
	}
	roundTripped, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&typed)
	if err != nil {
		t.Fatalf("to unstructured: %v", err)
	}

	missing := diffLeaves("", original, roundTripped, nil)
	if len(missing) == 0 {
		t.Fatal("differ failed to detect a deliberate field drop — meta-test broken")
	}
	hit := false
	for _, m := range missing {
		if strings.Contains(m, "spec.project") {
			hit = true
			break
		}
	}
	if !hit {
		t.Errorf("expected spec.project to be flagged; got %v", missing)
	}
}

// decodeAs is a tiny generic adapter so the test cases can hand back
// `any` without losing the concrete type at decode time. Returns a
// pointer because runtime.DefaultUnstructuredConverter.ToUnstructured
// requires a non-nil pointer on the way back.
func decodeAs[T any](raw []byte) (any, error) {
	var u unstructured.Unstructured
	if err := json.Unmarshal(raw, &u.Object); err != nil {
		return nil, fmt.Errorf("unmarshal unstructured: %w", err)
	}
	out := new(T)
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out); err != nil {
		return nil, fmt.Errorf("from unstructured: %w", err)
	}
	return out, nil
}

// diffLeaves walks `want` and reports leaf paths that are absent or
// changed in `got`. Maps are walked recursively; slices are walked
// index-by-index (with the path element shown as the index). Scalars
// are compared via reflect.DeepEqual after json normalisation, so
// int(2) and float64(2) both compare equal — JSON numbers are float64
// but typed structs decode to int, and we don't want that to look
// like a regression.
func diffLeaves(path string, want, got any, ignore map[string]struct{}) []string {
	if _, skip := ignore[path]; skip {
		return nil
	}

	switch w := want.(type) {
	case map[string]any:
		gMap, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: want map, got %T", path, got)}
		}
		var out []string
		for k, v := range w {
			child := k
			if path != "" {
				child = path + "." + k
			}
			gv, exists := gMap[k]
			if !exists {
				// Zero-value drops are operationally equivalent to
				// "field set to zero" — the operator's chart templates
				// can't distinguish `enabled: false` from missing
				// `enabled` because go's omitempty round-trip elides
				// zero values. We deliberately allow these so the
				// test catches *meaningful* data loss (a value that
				// can't be reconstructed) and not noise from json
				// encoder defaults.
				if isZeroJSONValue(v) {
					continue
				}
				out = append(out, fmt.Sprintf("%s: missing", child))
				continue
			}
			out = append(out, diffLeaves(child, v, gv, ignore)...)
		}
		return out
	case []any:
		gSlice, ok := got.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: want slice, got %T", path, got)}
		}
		if len(w) != len(gSlice) {
			return []string{fmt.Sprintf("%s: length %d → %d", path, len(w), len(gSlice))}
		}
		var out []string
		for i, v := range w {
			child := fmt.Sprintf("%s[%d]", path, i)
			out = append(out, diffLeaves(child, v, gSlice[i], ignore)...)
		}
		return out
	default:
		// Leaf compare. Normalise numbers — JSON's float64 vs Go's int
		// is the most common "looks like a regression but isn't" case.
		if normalise(w) != normalise(got) {
			// Fall back to DeepEqual for non-numeric values.
			if !reflect.DeepEqual(w, got) {
				return []string{fmt.Sprintf("%s: want %v (%T), got %v (%T)", path, w, w, got, got)}
			}
		}
		return nil
	}
}

// normalise turns numbers into float64 strings so int/float
// equivalents compare equal. Returns "" for non-numeric values so the
// caller can fall through to DeepEqual.
func normalise(v any) string {
	switch n := v.(type) {
	case float64:
		return fmt.Sprintf("num:%g", n)
	case int:
		return fmt.Sprintf("num:%g", float64(n))
	case int32:
		return fmt.Sprintf("num:%g", float64(n))
	case int64:
		return fmt.Sprintf("num:%g", float64(n))
	default:
		return ""
	}
}

// isZeroJSONValue reports whether v is the JSON zero value for its
// underlying type. Used to silence "field missing" errors when the
// original CR carried an explicit zero (e.g. `suspend: false`) and
// the round-trip dropped it via omitempty.
func isZeroJSONValue(v any) bool {
	if v == nil {
		return true
	}
	switch x := v.(type) {
	case bool:
		return !x
	case string:
		return x == ""
	case float64:
		return x == 0
	case int:
		return x == 0
	case int32:
		return x == 0
	case int64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	default:
		return false
	}
}

func asSet(s []string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, x := range s {
		out[x] = struct{}{}
	}
	return out
}
