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

func TestServiceSizeFlags(t *testing.T) {
	addCmds := []*cobra.Command{serviceAddCmd, serviceAddTopCmd}
	setCmds := []*cobra.Command{serviceSetCmd, serviceSetTopCmd}
	empty := map[string]any{}
	for _, tc := range []struct {
		name    string
		cmds    []*cobra.Command
		current map[string]any
		flags   [][2]string
		wantErr bool
		// wantRes is the expected body["resources"]; nil means the key
		// must be absent (the server default / live value is kept).
		wantRes map[string]any
	}{
		{
			name:  "ServiceAdd_SizeSendsPresetResources",
			cmds:  addCmds,
			flags: [][2]string{{"size", "large"}},
			wantRes: map[string]any{
				"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
				"limits":   map[string]any{"memory": "2Gi"},
			},
		},
		{name: "ServiceAdd_SizeNoneSendsEmptyResources", cmds: addCmds[:1], flags: [][2]string{{"size", "none"}}, wantRes: empty},
		{name: "ServiceAdd_NoSizeLeavesServerDefault", cmds: addCmds[:1]},
		{name: "ServiceAdd_UnknownSizeErrors", cmds: addCmds[:1], flags: [][2]string{{"size", "huge"}}, wantErr: true},
		// PATCH resources replaces verbatim, so a single-field flag must
		// merge onto the live resources rather than dropping the rest.
		{
			name: "ServiceSet_MemoryLimitMergesOntoCurrent",
			cmds: setCmds,
			current: map[string]any{
				"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
				"limits":   map[string]any{"memory": "1Gi"},
			},
			flags: [][2]string{{"memory-limit", "1536Mi"}},
			wantRes: map[string]any{
				"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
				"limits":   map[string]any{"memory": "1536Mi"},
			},
		},
		// --size replaces the live block (the old cpu limit is gone), then
		// the explicit flag wins over the preset.
		{
			name:    "ServiceSet_SizeThenOverride",
			cmds:    setCmds[:1],
			current: map[string]any{"limits": map[string]any{"cpu": "2"}},
			flags:   [][2]string{{"size", "small"}, {"cpu-request", "75m"}},
			wantRes: map[string]any{
				"requests": map[string]any{"cpu": "75m", "memory": "128Mi"},
				"limits":   map[string]any{"memory": "512Mi"},
			},
		},
		{
			name:    "ServiceSet_SizeNoneClears",
			cmds:    setCmds[:1],
			current: map[string]any{"limits": map[string]any{"memory": "1Gi"}},
			flags:   [][2]string{{"size", "none"}},
			wantRes: empty,
		},
		{name: "ServiceSet_NoSizeFlagsSendsNoResources", cmds: setCmds[:1], flags: [][2]string{{"display-name", "Web"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			sizeServer(t, tc.current, &body)
			resetFlags(t, tc.cmds, "size", "memory-limit", "cpu-request", "memory-request", "display-name")
			for _, cmd := range tc.cmds {
				for _, f := range tc.flags {
					mustSet(t, cmd, f[0], f[1])
				}
				err := cmd.RunE(cmd, []string{"acme", "web"})
				if tc.wantErr {
					if err == nil {
						t.Fatalf("%s: succeeded; want unknown-preset error", cmd.CommandPath())
					}
					if body != nil {
						t.Fatalf("%s: request sent despite the error: %v", cmd.CommandPath(), body)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s: %v", cmd.CommandPath(), err)
				}
				got, present := body["resources"]
				if tc.wantRes == nil {
					if present {
						t.Fatalf("%s: resources sent without size flags: %v", cmd.CommandPath(), got)
					}
					continue
				}
				if !reflect.DeepEqual(got, any(tc.wantRes)) {
					t.Fatalf("%s: resources = %#v, want %#v", cmd.CommandPath(), got, tc.wantRes)
				}
			}
		})
	}
}
