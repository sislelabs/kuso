package builds

import (
	"context"
	"net/http"
	"testing"
)

type recordRT struct{ got []string }

func (r *recordRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = append(r.got, req.Header.Get("Authorization"))
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

// The credential goes to the in-cluster registry only, never to an
// external registry host the same client might be pointed at.
func TestRegistryAuthTransport_OnlyInClusterHost(t *testing.T) {
	SetRegistryAuth(func(context.Context) (string, bool) { return "Basic abc", true })
	defer SetRegistryAuth(nil)
	rt := &recordRT{}
	c := &http.Client{Transport: registryAuthTransport{base: rt}}
	for _, u := range []string{"http://" + RegistryHost + "/v2/", "https://ghcr.io/v2/"} {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if rt.got[0] != "Basic abc" || rt.got[1] != "" {
		t.Fatalf("Authorization headers = %q", rt.got)
	}
}

func TestRegistryAuthTransport_AnonymousWithoutCredential(t *testing.T) {
	SetRegistryAuth(func(context.Context) (string, bool) { return "", false })
	defer SetRegistryAuth(nil)
	rt := &recordRT{}
	c := &http.Client{Transport: registryAuthTransport{base: rt}}
	resp, err := c.Get("http://" + RegistryHost + "/v2/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if rt.got[0] != "" {
		t.Fatalf("sent %q with no credential", rt.got[0])
	}
}
