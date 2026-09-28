package podsizes

import (
	"context"
	"reflect"
	"testing"

	"kuso/server/internal/db"
)

type fakeStore struct {
	sizes    []db.PodSize
	settings map[string]string
	creates  int
}

func (f *fakeStore) ListPodSizes(context.Context) ([]db.PodSize, error) {
	return append([]db.PodSize(nil), f.sizes...), nil
}

func (f *fakeStore) CreatePodSize(_ context.Context, p *db.PodSize) error {
	f.creates++
	f.sizes = append(f.sizes, *p)
	return nil
}

func (f *fakeStore) GetSetting(_ context.Context, key string) (string, error) {
	return f.settings[key], nil
}

func (f *fakeStore) SetSetting(_ context.Context, key, value, _ string) error {
	if f.settings == nil {
		f.settings = map[string]string{}
	}
	f.settings[key] = value
	return nil
}

func TestSeed_EmptyTableGetsThreePresetsOnce(t *testing.T) {
	st := &fakeStore{}
	n, err := Seed(context.Background(), st)
	if err != nil || n != 3 {
		t.Fatalf("first seed = %d, %v; want 3, nil", n, err)
	}
	n, err = Seed(context.Background(), st)
	if err != nil || n != 0 {
		t.Fatalf("second seed = %d, %v; want 0, nil", n, err)
	}
	if st.creates != 3 {
		t.Fatalf("creates = %d, want 3", st.creates)
	}
	for _, p := range st.sizes {
		if p.CPULimit != "" {
			t.Errorf("preset %s has a cpu limit %q; cpu limits throttle", p.Name, p.CPULimit)
		}
	}
}

func TestSeed_NonEmptyTableIsLeftAlone(t *testing.T) {
	st := &fakeStore{sizes: []db.PodSize{{ID: "x", Name: "custom"}}}
	n, err := Seed(context.Background(), st)
	if err != nil || n != 0 || st.creates != 0 {
		t.Fatalf("seed on non-empty = %d, %v, creates=%d; want 0, nil, 0", n, err, st.creates)
	}
}

func TestDefaultResources_MediumWhenUnset(t *testing.T) {
	st := &fakeStore{}
	if _, err := Seed(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	got, err := DefaultResources(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
		"limits":   map[string]any{"memory": "1Gi"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default resources = %v, want %v", got, want)
	}
}

func TestDefaultResources_NoneDisables(t *testing.T) {
	st := &fakeStore{}
	if _, err := Seed(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if err := SetDefault(context.Background(), st, "none", "t"); err != nil {
		t.Fatal(err)
	}
	got, err := DefaultResources(context.Background(), st)
	if err != nil || got != nil {
		t.Fatalf("resources with default=none = %v, %v; want nil", got, err)
	}
}

func TestSetDefault_RejectsUnknownPreset(t *testing.T) {
	st := &fakeStore{}
	if _, err := Seed(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if err := SetDefault(context.Background(), st, "huge", "t"); err == nil {
		t.Fatal("SetDefault(huge) succeeded; want an error for an unknown preset")
	}
	if err := SetDefault(context.Background(), st, "large", "t"); err != nil {
		t.Fatal(err)
	}
	got, _ := DefaultResources(context.Background(), st)
	if lim := got["limits"].(map[string]any)["memory"]; lim != "2Gi" {
		t.Fatalf("large limit = %v, want 2Gi", lim)
	}
}

// A default naming a preset that was later deleted must not block service
// creation: it yields no resources rather than an error.
func TestDefaultResources_DeletedPresetYieldsNil(t *testing.T) {
	st := &fakeStore{settings: map[string]string{SettingKey: "gone"}}
	got, err := DefaultResources(context.Background(), st)
	if err != nil || got != nil {
		t.Fatalf("resources for deleted preset = %v, %v; want nil, nil", got, err)
	}
}

func TestResources_OmitsEmptyQuantities(t *testing.T) {
	got := Resources(db.PodSize{CPURequest: "50m", MemoryRequest: "128Mi", MemoryLimit: "512Mi"})
	want := map[string]any{
		"requests": map[string]any{"cpu": "50m", "memory": "128Mi"},
		"limits":   map[string]any{"memory": "512Mi"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Resources = %v, want %v", got, want)
	}
	if got := Resources(db.PodSize{}); got != nil {
		t.Fatalf("Resources(empty) = %v, want nil", got)
	}
}

func TestResourcesFor_NamedPreset(t *testing.T) {
	st := &fakeStore{}
	if _, err := Seed(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ResourcesFor(context.Background(), st, "large")
	if err != nil || !ok {
		t.Fatalf("large: ok=%v err=%v", ok, err)
	}
	lim, _ := got["limits"].(map[string]any)
	if lim["memory"] != "2Gi" {
		t.Fatalf("large limits = %v, want memory 2Gi", got["limits"])
	}
	if _, ok, err := ResourcesFor(context.Background(), st, "gigantic"); ok || err != nil {
		t.Fatalf("unknown preset: ok=%v err=%v, want false, nil", ok, err)
	}
}
