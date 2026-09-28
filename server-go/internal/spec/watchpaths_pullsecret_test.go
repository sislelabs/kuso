package spec

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func TestSpec_WatchPathsAndPullSecretRoundTrip(t *testing.T) {
	t.Parallel()
	f, err := Parse([]byte(`
project: shop
services:
  - name: web
    repo: https://github.com/acme/mono
    path: apps/web
    watchPaths: ["apps/web/**", "packages/ui/**"]
  - name: api
    runtime: image
    image: { repository: ghcr.io/acme/api, tag: v1, pullSecret: ghcr.io }
`))
	if err != nil {
		t.Fatal(err)
	}
	web, api := f.Services[0], f.Services[1]

	if got := serviceCreateReq(web).WatchPaths; len(got) != 2 {
		t.Errorf("create watchPaths = %q", got)
	}
	if ps := serviceCreateReq(api).Image.PullSecret; ps == nil || *ps != "ghcr.io" {
		t.Errorf("create image.pullSecret = %v", ps)
	}

	live := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-web"},
		Spec:       kube.KusoServiceSpec{Project: "shop", Repo: &kube.KusoRepoRef{URL: "https://github.com/acme/mono", Path: "apps/web"}},
	}
	req, changes := diffServiceSpec(live, web)
	if req.WatchPaths == nil || !hasChange(changes, "watchPaths") {
		t.Errorf("watchPaths-only change must be planned + patched; changes=%+v", changes)
	}
	live.Spec.WatchPaths = []string{"apps/web/**", "packages/ui/**"}
	if req, changes = diffServiceSpec(live, web); req.WatchPaths != nil || hasChange(changes, "watchPaths") {
		t.Errorf("unchanged watchPaths must not be planned; changes=%+v", changes)
	}

	liveAPI := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-api"},
		Spec: kube.KusoServiceSpec{Project: "shop", Runtime: "image",
			Image: &kube.KusoImage{Repository: "ghcr.io/acme/api", Tag: "v1"}},
	}
	if req, changes = diffServiceSpec(liveAPI, api); req.Image == nil || !hasChange(changes, "image") {
		t.Errorf("adding a pull secret must be planned; changes=%+v", changes)
	}
	// Live holds the resolved Secret name; the file says the host. Same thing.
	liveAPI.Spec.Image.PullSecret = "shop-regcred-ghcr-io"
	if req, changes = diffServiceSpec(liveAPI, api); req.Image != nil || hasChange(changes, "image") {
		t.Errorf("host vs resolved secret name must compare equal; changes=%+v", changes)
	}

	exported := exportService("shop", *liveAPI)
	if exported.Image == nil || exported.Image.PullSecret != "shop-regcred-ghcr-io" {
		t.Errorf("export image = %+v", exported.Image)
	}
	liveWeb := *live
	if got := exportService("shop", liveWeb).WatchPaths; len(got) != 2 {
		t.Errorf("export watchPaths = %q", got)
	}
}

func hasChange(changes []FieldChange, field string) bool {
	for _, c := range changes {
		if c.Field == field {
			return true
		}
	}
	return false
}
