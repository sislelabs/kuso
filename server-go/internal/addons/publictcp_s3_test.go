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
	if _, err := s.EnablePublicTCP(context.Background(), "alpha", "files", PublicTCPOptions{}); !errors.Is(err, ErrPublicTCPS3Plaintext) {
		t.Errorf("new s3 exposure: err = %v, want ErrPublicTCPS3Plaintext", err)
	}
	pt, err := s.EnablePublicTCP(context.Background(), "alpha", "legacy", PublicTCPOptions{})
	if err != nil || pt.Port != 30004 {
		t.Errorf("existing s3 exposure: publicTCP=%+v err=%v, want port 30004", pt, err)
	}
}
