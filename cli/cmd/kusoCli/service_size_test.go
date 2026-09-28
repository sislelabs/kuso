package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

const podSizesJSON = `[
 {"ID":"podsize-small","Name":"small","CPULimit":"","MemoryLimit":"512Mi","CPURequest":"50m","MemoryRequest":"128Mi"},
 {"ID":"podsize-large","Name":"large","CPULimit":"","MemoryLimit":"2Gi","CPURequest":"250m","MemoryRequest":"512Mi"}
]`

// sizeServer serves the preset list and the live service (whose spec
// carries current), and captures the POST/PATCH body.
func sizeServer(t *testing.T, current map[string]any, body *map[string]any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/config/podsizes":
			_, _ = io.WriteString(w, podSizesJSON)
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"spec": map[string]any{"resources": current}})
		default:
			defer r.Body.Close()
			*body = nil
			if err := json.NewDecoder(r.Body).Decode(body); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			_, _ = io.WriteString(w, "{}")
		}
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil })
}

func resetFlags(t *testing.T, cmds []*cobra.Command, names ...string) {
	t.Cleanup(func() {
		serviceAddSize, serviceSetSize, serviceSetDisplayName = "", "", ""
		serviceSetMemLimit, serviceSetMemRequest, serviceSetCPURequest = "", "", ""
		for _, c := range cmds {
			for _, n := range names {
				if f := c.Flags().Lookup(n); f != nil {
					f.Changed = false
				}
			}
		}
	})
}

func TestServiceAdd_SizeSendsPresetResources(t *testing.T) {
	var body map[string]any
	sizeServer(t, nil, &body)
	cmds := []*cobra.Command{serviceAddCmd, serviceAddTopCmd}
	resetFlags(t, cmds, "size")

	want := map[string]any{
		"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
		"limits":   map[string]any{"memory": "2Gi"},
	}
	for _, cmd := range cmds {
		mustSet(t, cmd, "size", "large")
		if err := cmd.RunE(cmd, []string{"acme", "web"}); err != nil {
			t.Fatalf("%s: %v", cmd.CommandPath(), err)
		}
		if !reflect.DeepEqual(body["resources"], want) {
			t.Fatalf("%s: resources = %v, want %v", cmd.CommandPath(), body["resources"], want)
		}
	}
}

func TestServiceAdd_SizeNoneSendsEmptyResources(t *testing.T) {
	var body map[string]any
	sizeServer(t, nil, &body)
	resetFlags(t, []*cobra.Command{serviceAddCmd}, "size")
	mustSet(t, serviceAddCmd, "size", "none")
	if err := serviceAddCmd.RunE(serviceAddCmd, []string{"acme", "web"}); err != nil {
		t.Fatal(err)
	}
	res, ok := body["resources"].(map[string]any)
	if !ok || len(res) != 0 {
		t.Fatalf("resources = %#v, want {} (explicit none)", body["resources"])
	}
}

func TestServiceAdd_NoSizeLeavesServerDefault(t *testing.T) {
	var body map[string]any
	sizeServer(t, nil, &body)
	resetFlags(t, []*cobra.Command{serviceAddCmd}, "size")
	if err := serviceAddCmd.RunE(serviceAddCmd, []string{"acme", "web"}); err != nil {
		t.Fatal(err)
	}
	if _, present := body["resources"]; present {
		t.Fatalf("resources sent without --size: %v", body["resources"])
	}
}

func TestServiceAdd_UnknownSizeErrors(t *testing.T) {
	var body map[string]any
	sizeServer(t, nil, &body)
	resetFlags(t, []*cobra.Command{serviceAddCmd}, "size")
	mustSet(t, serviceAddCmd, "size", "huge")
	if err := serviceAddCmd.RunE(serviceAddCmd, []string{"acme", "web"}); err == nil {
		t.Fatal("--size huge succeeded; want unknown-preset error")
	}
	if body != nil {
		t.Fatalf("request sent despite unknown preset: %v", body)
	}
}

// PATCH resources replaces verbatim, so a single-field flag must merge onto
// the live resources rather than dropping the rest.
func TestServiceSet_MemoryLimitMergesOntoCurrent(t *testing.T) {
	var body map[string]any
	current := map[string]any{
		"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
		"limits":   map[string]any{"memory": "1Gi"},
	}
	sizeServer(t, current, &body)
	cmds := []*cobra.Command{serviceSetCmd, serviceSetTopCmd}
	resetFlags(t, cmds, "memory-limit", "cpu-request", "size", "memory-request")
	for _, cmd := range cmds {
		mustSet(t, cmd, "memory-limit", "1536Mi")
		if err := cmd.RunE(cmd, []string{"acme", "web"}); err != nil {
			t.Fatalf("%s: %v", cmd.CommandPath(), err)
		}
		want := map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
			"limits":   map[string]any{"memory": "1536Mi"},
		}
		if !reflect.DeepEqual(body["resources"], want) {
			t.Fatalf("%s: resources = %v, want %v", cmd.CommandPath(), body["resources"], want)
		}
	}
}

func TestServiceSet_SizeThenOverride(t *testing.T) {
	var body map[string]any
	sizeServer(t, map[string]any{"limits": map[string]any{"cpu": "2"}}, &body)
	resetFlags(t, []*cobra.Command{serviceSetCmd}, "size", "cpu-request")
	mustSet(t, serviceSetCmd, "size", "small")
	mustSet(t, serviceSetCmd, "cpu-request", "75m")
	if err := serviceSetCmd.RunE(serviceSetCmd, []string{"acme", "web"}); err != nil {
		t.Fatal(err)
	}
	// --size replaces the live block (the old cpu limit is gone), then the
	// explicit flag wins over the preset.
	want := map[string]any{
		"requests": map[string]any{"cpu": "75m", "memory": "128Mi"},
		"limits":   map[string]any{"memory": "512Mi"},
	}
	if !reflect.DeepEqual(body["resources"], want) {
		t.Fatalf("resources = %v, want %v", body["resources"], want)
	}
}

func TestServiceSet_SizeNoneClears(t *testing.T) {
	var body map[string]any
	sizeServer(t, map[string]any{"limits": map[string]any{"memory": "1Gi"}}, &body)
	resetFlags(t, []*cobra.Command{serviceSetCmd}, "size")
	mustSet(t, serviceSetCmd, "size", "none")
	if err := serviceSetCmd.RunE(serviceSetCmd, []string{"acme", "web"}); err != nil {
		t.Fatal(err)
	}
	res, ok := body["resources"].(map[string]any)
	if !ok || len(res) != 0 {
		t.Fatalf("resources = %#v, want {} (clears)", body["resources"])
	}
}

func TestServiceSet_NoSizeFlagsSendsNoResources(t *testing.T) {
	var body map[string]any
	sizeServer(t, nil, &body)
	resetFlags(t, []*cobra.Command{serviceSetCmd}, "display-name")
	mustSet(t, serviceSetCmd, "display-name", "Web")
	if err := serviceSetCmd.RunE(serviceSetCmd, []string{"acme", "web"}); err != nil {
		t.Fatal(err)
	}
	if _, present := body["resources"]; present {
		t.Fatalf("resources sent without size flags: %v", body["resources"])
	}
}
