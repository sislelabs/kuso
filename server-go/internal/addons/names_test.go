package addons

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Helm refuses release names over 53 characters, and an addon's PR
// preview clone appends "-pr-N"; the API accepted names that could never
// render.
func TestAdd_RefusesNameTooLongForHelm(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	long := strings.Repeat("d", 40) // alpha-ddd… = 46, plus -pr-99999 > 53
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: long, Kind: "postgres"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// Addon "web" and service "web" are both helm release alpha-web; the
// second CR never installs while the API says 201.
func TestAdd_RefusesNameHeldByService(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"), typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web", Namespace: "kuso"},
	}))
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "web", Kind: "postgres"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}
