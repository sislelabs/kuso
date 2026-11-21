package addons

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"kuso/server/internal/kube"
)

func ptr[T any](v T) *T { return &v }

func TestPublicTCPOptions_Normalize(t *testing.T) {
	t.Parallel()
	tooMany := make([]string, maxPublicTCPSourceRanges+1)
	for i := range tooMany {
		tooMany[i] = "10.0.0.0/8"
	}
	for _, tc := range []struct {
		name    string
		in      PublicTCPOptions
		want    []string
		wantErr bool
	}{
		{name: "unset", in: PublicTCPOptions{}},
		{name: "v4 and v6", in: PublicTCPOptions{SourceRanges: ptr([]string{"203.0.113.7/32", "2001:db8::/32"})},
			want: []string{"203.0.113.7/32", "2001:db8::/32"}},
		{name: "canonicalised and deduped", in: PublicTCPOptions{SourceRanges: ptr([]string{" 10.1.2.3/8", "10.0.0.0/8"})},
			want: []string{"10.0.0.0/8"}},
		{name: "explicit clear", in: PublicTCPOptions{SourceRanges: ptr([]string{})}, want: []string{}},
		{name: "bare IP", in: PublicTCPOptions{SourceRanges: ptr([]string{"203.0.113.7"})}, wantErr: true},
		{name: "garbage", in: PublicTCPOptions{SourceRanges: ptr([]string{"10.0.0.0/8", "office"})}, wantErr: true},
		{name: "bad prefix", in: PublicTCPOptions{SourceRanges: ptr([]string{"10.0.0.0/33"})}, wantErr: true},
		{name: "too many", in: PublicTCPOptions{SourceRanges: &tooMany}, wantErr: true},
		{name: "max conns ok", in: PublicTCPOptions{MaxConnections: ptr(int32(50))}},
		{name: "max conns zero clears", in: PublicTCPOptions{MaxConnections: ptr(int32(0))}},
		{name: "max conns negative", in: PublicTCPOptions{MaxConnections: ptr(int32(-1))}, wantErr: true},
		{name: "max conns over cap", in: PublicTCPOptions{MaxConnections: ptr(int32(maxPublicTCPConnections + 1))}, wantErr: true},
	} {
		got, err := tc.in.normalize()
		if tc.wantErr {
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected err %v", tc.name, err)
			continue
		}
		if tc.want == nil {
			if got.SourceRanges != nil {
				t.Errorf("%s: SourceRanges = %v, want nil", tc.name, *got.SourceRanges)
			}
			continue
		}
		if got.SourceRanges == nil || !reflect.DeepEqual(*got.SourceRanges, tc.want) {
			t.Errorf("%s: SourceRanges = %v, want %v", tc.name, got.SourceRanges, tc.want)
		}
	}
}

func TestEnablePublicTCP_Restrictions(t *testing.T) {
	t.Setenv("KUSO_TCP_PROXY_PORTS", "30000-30010")
	s := fakeService(t, seedProj("alpha"),
		seedAddonSpec("alpha", "pg", kube.KusoAddonSpec{Kind: "postgres"}))
	ctx := context.Background()
	read := func() kube.KusoAddonPublicTCP {
		t.Helper()
		a, err := s.Kube.GetKusoAddon(ctx, "kuso", "alpha-pg")
		if err != nil {
			t.Fatal(err)
		}
		if a.Spec.PublicTCP == nil {
			t.Fatal("publicTCP block missing")
		}
		return *a.Spec.PublicTCP
	}

	// First enable allocates the port and lands the restrictions in the
	// same write.
	enable := func(opts PublicTCPOptions) kube.KusoAddonPublicTCP {
		t.Helper()
		pt, err := s.EnablePublicTCP(ctx, "alpha", "pg", opts)
		if err != nil {
			t.Fatal(err)
		}
		return *pt
	}
	got := enable(PublicTCPOptions{
		SourceRanges:   ptr([]string{"203.0.113.9/24"}),
		MaxConnections: ptr(int32(20)),
	})
	want := kube.KusoAddonPublicTCP{Enabled: true, Port: 30000, SourceRanges: []string{"203.0.113.0/24"}, MaxConnections: 20}
	if stored := read(); !reflect.DeepEqual(got, want) || !reflect.DeepEqual(stored, want) {
		t.Fatalf("after enable: returned %+v, stored %+v, want %+v", got, stored, want)
	}

	// Re-enable with no options keeps everything.
	if got := enable(PublicTCPOptions{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("re-enable returned %+v, want %+v", got, want)
	}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after no-op re-enable: %+v, want %+v", got, want)
	}

	// A bad CIDR on an enabled addon is refused and changes nothing.
	_, err := s.EnablePublicTCP(ctx, "alpha", "pg", PublicTCPOptions{SourceRanges: ptr([]string{"nope"})})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("bad cidr: err = %v, want ErrInvalid naming the input", err)
	}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after rejected update: %+v, want %+v", got, want)
	}

	// Clearing the allow-list leaves the connection cap and the port alone.
	got = enable(PublicTCPOptions{SourceRanges: ptr([]string{})})
	want.SourceRanges = nil
	if stored := read(); !reflect.DeepEqual(got, want) || !reflect.DeepEqual(stored, want) {
		t.Fatalf("after clear: returned %+v, stored %+v, want %+v", got, stored, want)
	}
}
