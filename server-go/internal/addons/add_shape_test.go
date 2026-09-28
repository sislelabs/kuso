package addons

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestAdd_RejectsBadShapeWithAllowedValues: a typo'd kind or size, or a name
// kube would reject, is a 400-class ErrInvalid naming the accepted values,
// not a CRD 422 that surfaces as a 500.
func TestAdd_RejectsBadShapeWithAllowedValues(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	cases := []struct {
		name string
		req  CreateAddonRequest
		want []string
	}{
		{"unknown kind", CreateAddonRequest{Name: "db", Kind: "postgresql"}, []string{`"postgresql"`, "postgres, redis", "redpanda"}},
		{"unknown size", CreateAddonRequest{Name: "db", Kind: "postgres", Size: "xl"}, []string{`"xl"`, "small, medium, large"}},
		{"uppercase name", CreateAddonRequest{Name: "MyDB", Kind: "postgres"}, []string{`"MyDB"`, "lowercase"}},
		{"underscore name", CreateAddonRequest{Name: "my_db", Kind: "postgres"}, []string{`"my_db"`}},
	}
	for _, tc := range cases {
		_, err := s.Add(context.Background(), "alpha", tc.req)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", tc.name, err)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: message %q missing %q", tc.name, err.Error(), w)
			}
		}
	}
}

// TestAdd_AcceptsEveryCRDKindAndSize: the up-front check must not be
// stricter than the CRD enum for kind and size.
func TestAdd_AcceptsEveryCRDKindAndSize(t *testing.T) {
	t.Parallel()
	for _, k := range append(append([]string{}, SupportedKinds...), reservedKinds...) {
		if err := validateCreateShape(CreateAddonRequest{Name: "db-pr-35", Kind: k}); err != nil {
			t.Errorf("kind %s: %v", k, err)
		}
	}
	for _, sz := range Sizes {
		if err := validateCreateShape(CreateAddonRequest{Name: "db", Kind: "postgres", Size: sz}); err != nil {
			t.Errorf("size %s: %v", sz, err)
		}
	}
}
