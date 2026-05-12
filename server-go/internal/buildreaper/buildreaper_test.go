package buildreaper

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestIsDone(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		lbls map[string]string
		want bool
	}{
		{
			name: "spec.done=true",
			spec: map[string]any{"done": true},
			want: true,
		},
		{
			name: "build-state=done label",
			lbls: map[string]string{"kuso.sislelabs.com/build-state": "done"},
			want: true,
		},
		{
			name: "both signals",
			spec: map[string]any{"done": true},
			lbls: map[string]string{"kuso.sislelabs.com/build-state": "done"},
			want: true,
		},
		{
			name: "spec.done=false explicit",
			spec: map[string]any{"done": false},
			want: false,
		},
		{
			name: "no signal — fresh CR",
			spec: map[string]any{"image": map[string]any{"tag": "v1"}},
			want: false,
		},
		{
			name: "unrelated label",
			lbls: map[string]string{"kuso.sislelabs.com/project": "alpha"},
			want: false,
		},
		{
			name: "empty",
			want: false,
		},
	}
	for _, c := range cases {
		u := &unstructured.Unstructured{Object: map[string]any{}}
		if c.spec != nil {
			u.Object["spec"] = c.spec
		}
		if c.lbls != nil {
			u.SetLabels(c.lbls)
		}
		u.SetName("test")
		u.SetNamespace("default")
		// Ensure round-trippable metadata.
		u.Object["metadata"] = u.Object["metadata"]
		if got := isDone(u); got != c.want {
			t.Errorf("%s: isDone = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsDoneNil(t *testing.T) {
	if isDone(nil) {
		t.Error("isDone(nil) should be false")
	}
}

func TestIsDoneLabelsCoexistWithMetadata(t *testing.T) {
	// Sanity check that SetLabels + spec coexist on the same object.
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetName("b")
	u.SetNamespace("ns")
	u.SetLabels(map[string]string{"kuso.sislelabs.com/build-state": "done"})
	u.Object["spec"] = map[string]any{"image": map[string]any{"tag": "v1"}}
	if !isDone(u) {
		t.Error("expected isDone=true when label says done")
	}
	// Confirm metadata wasn't clobbered.
	if u.GetName() != "b" {
		t.Errorf("name lost: got %q", u.GetName())
	}
	_ = metav1.Time{}
}
