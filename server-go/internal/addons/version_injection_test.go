package addons

import (
	"context"
	"errors"
	"testing"
)

// spec.version is rendered inside a quoted `image:` line in every engine
// template. A quote + newline + `---` would inject a whole extra manifest
// (a privileged hostPath Pod was reproducible), so Add must reject it.
func TestAdd_RejectsVersionThatCouldInjectManifests(t *testing.T) {
	for _, v := range []string{
		"7-alpine\"\n---\napiVersion: v1\nkind: Pod",
		"16\nfoo: bar",
		"16 ",
		"16\"",
		"{{ .Release.Name }}",
		"-16",
		"16:latest",
		"a/b",
	} {
		s := fakeService(t, seedProj("alpha"))
		_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres", Version: v})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("version %q: got %v, want ErrInvalid", v, err)
		}
	}
}

func TestAdd_AcceptsRealEngineVersions(t *testing.T) {
	for _, v := range []string{"", "16", "17", "7.2", "8.0.39", "v1.11.1", "RELEASE.2024-10-13T13-34-11Z", "v24.2.7", "3.13"} {
		s := fakeService(t, seedProj("alpha"))
		if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres", Version: v}); err != nil {
			t.Errorf("version %q: unexpected error %v", v, err)
		}
	}
}
