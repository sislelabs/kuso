package addons

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// Enabling public TCP on a kind the chart can't route made the addon's
// render fail, which froze every later spec change to it. The API must
// refuse those kinds up front.
func TestEnablePublicTCP_RejectsUnsupportedKinds(t *testing.T) {
	t.Setenv("KUSO_TCP_PROXY_PORTS", "30000-30010")
	s := fakeService(t, seedProj("alpha"),
		seedAddonSpec("alpha", "mail", kube.KusoAddonSpec{Kind: "mailpit", Project: "alpha"}),
		seedAddonSpec("alpha", "cache", kube.KusoAddonSpec{Kind: "redis", Project: "alpha", HA: true}),
		seedAddonSpec("alpha", "q", kube.KusoAddonSpec{Kind: "valkey", Project: "alpha"}),
	)
	for _, name := range []string{"mail", "cache"} {
		if _, err := s.EnablePublicTCP(context.Background(), "alpha", name); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	port, err := s.EnablePublicTCP(context.Background(), "alpha", "q")
	if err != nil || port != 30000 {
		t.Fatalf("valkey: port=%d err=%v, want 30000", port, err)
	}
}

func TestCheckPublicTCPSupported(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		spec kube.KusoAddonSpec
		ok   bool
	}{
		{kube.KusoAddonSpec{Kind: "postgres"}, true},
		{kube.KusoAddonSpec{Kind: "postgres", HA: true}, true},
		{kube.KusoAddonSpec{Kind: "nats", HA: true}, true},
		{kube.KusoAddonSpec{Kind: "rabbitmq"}, true},
		{kube.KusoAddonSpec{Kind: "mailpit"}, false},
		{kube.KusoAddonSpec{Kind: "kafka"}, false},
		{kube.KusoAddonSpec{Kind: "mongodb", HA: true}, false},
		{kube.KusoAddonSpec{Kind: "postgres", UseInstanceAddon: "pg"}, false},
		{kube.KusoAddonSpec{Kind: "postgres", External: &kube.KusoAddonExternal{SecretName: "s"}}, false},
	} {
		if err := checkPublicTCPSupported(&tc.spec); (err == nil) != tc.ok {
			t.Errorf("%+v: err=%v, want ok=%v", tc.spec, err, tc.ok)
		}
	}
}
