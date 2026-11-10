package addons

import (
	"context"
	"errors"
	"testing"
)

// K8S-10/A14: the s3 bucket name reaches a shell and policy JSON; the
// Postgres identifier rule let `$` and upper case through.
func TestAdd_S3BucketNameValidated(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	for _, bad := range []string{"Files", "fi$les", "a", "-files", "files-"} {
		_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "files", Kind: "s3", Database: bad})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("bucket %q: got %v, want ErrInvalid", bad, err)
		}
	}
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "files", Kind: "s3", Database: "user_uploads"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("underscore is not a valid bucket name, got %v", err)
	}
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "files", Kind: "s3", Database: "uploads2"}); errors.Is(err, ErrInvalid) {
		t.Errorf("valid bucket rejected: %v", err)
	}
}
