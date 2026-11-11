package addons

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// BLOB-R3: MinIO serves plain HTTP, so a new public port on s3 is refused,
// while an exposure made before the rule keeps its port.
func TestEnablePublicTCP_S3RefusedButExistingKept(t *testing.T) {
	t.Setenv("KUSO_TCP_PROXY_PORTS", "30000-30010")
	s := fakeService(t, seedProj("alpha"),
		seedAddonSpec("alpha", "files", kube.KusoAddonSpec{Kind: "s3", Project: "alpha"}),
		seedAddonSpec("alpha", "legacy", kube.KusoAddonSpec{Kind: "s3", Project: "alpha",
			PublicTCP: &kube.KusoAddonPublicTCP{Enabled: true, Port: 30004}}),
	)
	if _, err := s.EnablePublicTCP(context.Background(), "alpha", "files"); !errors.Is(err, ErrPublicTCPS3Plaintext) {
		t.Errorf("new s3 exposure: err = %v, want ErrPublicTCPS3Plaintext", err)
	}
	port, err := s.EnablePublicTCP(context.Background(), "alpha", "legacy")
	if err != nil || port != 30004 {
		t.Errorf("existing s3 exposure: port=%d err=%v, want 30004", port, err)
	}
}
