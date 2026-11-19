package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"kuso/pkg/kusoApi"
)

func TestSplitCmd(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// Plain argv splits on whitespace.
		{"rails runner Cleanup.run", []string{"rails", "runner", "Cleanup.run"}},
		// Quoted segment stays one arg — the case strings.Fields broke.
		{`sh -c "echo tick"`, []string{"sh", "-c", "echo tick"}},
		{`sh -c 'echo tick'`, []string{"sh", "-c", "echo tick"}},
		// Quoted string containing multiple spaces is preserved verbatim.
		{`sh -c "rm -rf /tmp/*"`, []string{"sh", "-c", "rm -rf /tmp/*"}},
		// Empty input yields empty argv, not a one-element slice.
		{"", nil},
		// Unbalanced quote falls back to Fields (best-effort, no error).
		{`sh -c "oops`, []string{"sh", "-c", `"oops`}},
	}
	for _, c := range cases {
		got := splitCmd(c.in)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCmd(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

// spec.image is replaced whole server-side, so a tag-only edit must
// carry the live pull secret (and repo/policy) or the next run of a
// private image is ImagePullBackOff.
func TestCronEditImageTagKeepsPullSecret(t *testing.T) {
	var patched kusoApi.UpdateProjectCronRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &patched)
			_, _ = io.WriteString(w, `{}`)
			return
		}
		_, _ = io.WriteString(w, `[{"metadata":{"name":"shop-sync"},"spec":{"image":{"repository":"ghcr.io/acme/sync","tag":"v3","pullPolicy":"Always","pullSecret":"regcred"}}}]`)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() {
		api = nil
		cronEditCmd.Flags().Lookup("image-tag").Changed = false
		pCronImageTag = "latest"
	})

	captureStdout(t, func() {
		if _, err := runRoot(t, "cron", "edit", "shop", "sync", "--image-tag", "v4"); err != nil {
			t.Fatal(err)
		}
	})
	img := patched.Image
	if img == nil || img.Repository != "ghcr.io/acme/sync" || img.Tag != "v4" || img.PullPolicy != "Always" || img.PullSecret != "regcred" {
		t.Fatalf("image %+v", img)
	}
}
