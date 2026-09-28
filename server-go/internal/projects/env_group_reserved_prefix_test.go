package projects

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestCreateEnvGroup_ReservedPreviewPrefixes — pr-* and preview-* belong to
// webhook-driven PR previews; both are refused before any kube call.
func TestCreateEnvGroup_ReservedPreviewPrefixes(t *testing.T) {
	s := &Service{}
	for _, name := range []string{"pr-12", "preview-pr-12", "preview-x"} {
		_, err := s.CreateEnvGroup(context.Background(), "shop", CreateEnvGroupRequest{Name: name})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "PR preview") {
			t.Errorf("%s: got %v, want ErrInvalid naming the PR preview reservation", name, err)
		}
	}
}
