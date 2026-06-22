package builds

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// TestBuildRichCard_Succeeded covers the happy-path build card: title
// in "<glyph> <verb> · <project>/<service>" shape, description from
// the commit message (first line only), three inline fields in order
// Ref / By / Built in.
func TestBuildRichCard_Succeeded(t *testing.T) {
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "distill-web-abc123",
			Annotations: map[string]string{
				annCommitMessage: "feat(brand): real Papelito mark\n\nlonger body\nignored",
				annTriggerUser:   "ivo9999",
				annStartedAt:     "2026-05-16T12:00:00Z",
				annCompletedAt:   "2026-05-16T12:01:24Z",
			},
		},
		Spec: kube.KusoBuildSpec{
			Project: "distill",
			Service: "distill-web",
			Branch:  "main",
			Ref:     "53d3f34262ef",
		},
	}
	title, desc, fields := buildRichCard(b, "web", "succeeded", "", "")
	if title != "✓ Build succeeded · distill / web" {
		t.Errorf("title: %q", title)
	}
	if desc != "feat(brand): real Papelito mark" {
		t.Errorf("description should be first line only: %q", desc)
	}
	if len(fields) != 3 {
		t.Fatalf("expected 3 fields, got %d (%+v)", len(fields), fields)
	}
	if fields[0].Name != "Ref" || fields[0].Value != "`main` · `53d3f34`" {
		t.Errorf("ref field: %+v", fields[0])
	}
	if fields[1].Name != "By" || fields[1].Value != "ivo9999" {
		t.Errorf("by field: %+v", fields[1])
	}
	if fields[2].Name != "Built in" || fields[2].Value != "1m 24s" {
		t.Errorf("duration field: %+v", fields[2])
	}
}

// TestBuildRichCard_Failed verifies failed-build cards use the failure
// reason as the description when no commit message is available, and
// flip the duration label.
func TestBuildRichCard_Failed(t *testing.T) {
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				annStartedAt:   "2026-05-16T12:00:00Z",
				annCompletedAt: "2026-05-16T12:00:42Z",
			},
		},
		Spec: kube.KusoBuildSpec{
			Project: "distill",
			Service: "distill-web",
			Branch:  "main",
			Ref:     "abc1234",
		},
	}
	title, desc, fields := buildRichCard(b, "web", "failed", "kaniko: COPY failed: not found", "")
	if title != "✗ Build failed · distill / web" {
		t.Errorf("title: %q", title)
	}
	if desc != "kaniko: COPY failed: not found" {
		t.Errorf("description should fall back to failure reason: %q", desc)
	}
	// No annTriggerUser → no "By" field. So we expect 2 fields.
	if len(fields) != 2 {
		t.Fatalf("expected 2 fields, got %d (%+v)", len(fields), fields)
	}
	if fields[1].Name != "Failed after" || fields[1].Value != "42s" {
		t.Errorf("duration label/value: %+v", fields[1])
	}
}

// TestBuildRichCard_SyntheticRef verifies that a redeploy-triggered
// build (no real SHA, ref of "<branch>-<base36>") collapses the ref
// field to just the branch and synthesises a "Manual redeploy" desc.
// Without this the user sees nonsensical "main · main-mp" in Discord.
func TestBuildRichCard_SyntheticRef(t *testing.T) {
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				annTriggerUser: "ivo9999",
				annStartedAt:   "2026-05-16T12:00:00Z",
				annCompletedAt: "2026-05-16T12:00:12Z",
			},
		},
		Spec: kube.KusoBuildSpec{
			Project: "distill",
			Service: "distill-web",
			Branch:  "main",
			Ref:     "main-mp81chv5", // synthetic — branch prefix + base36 suffix
		},
	}
	_, desc, fields := buildRichCard(b, "web", "succeeded", "", "")
	if desc != "Manual redeploy of `main` by ivo9999" {
		t.Errorf("synthetic-ref description: %q", desc)
	}
	// Ref field shows only the branch, not the synth suffix.
	if fields[0].Name != "Ref" || fields[0].Value != "`main`" {
		t.Errorf("synth-ref field should hide the suffix: %+v", fields[0])
	}
}

// TestBuildRichCard_SiteURL verifies the Site field is appended for
// succeeded builds with a configured public URL — and stripped of the
// scheme for display, with the full URL preserved in the markdown
// link target.
func TestBuildRichCard_SiteURL(t *testing.T) {
	b := &kube.KusoBuild{
		Spec: kube.KusoBuildSpec{
			Project: "distill",
			Service: "distill-web",
			Branch:  "main",
			Ref:     "53d3f34262ef",
		},
	}
	_, _, fields := buildRichCard(b, "web", "succeeded", "", "https://web.distill.sislelabs.com")
	var siteField *EnvelopeField
	for i := range fields {
		if fields[i].Name == "Site" {
			siteField = &fields[i]
		}
	}
	if siteField == nil {
		t.Fatalf("Site field missing on succeeded build")
	}
	if siteField.Value != "[web.distill.sislelabs.com](https://web.distill.sislelabs.com)" {
		t.Errorf("Site field value: %q", siteField.Value)
	}
}

// TestLookupSiteURL covers the resolution order: explicit service domain
// first, then the production env's auto-generated host, then "" for an
// internal-only service or no env at all. The env fallback is the fix for
// auto-host services (e.g. scaffold) whose Site link was silently dropped.
func TestLookupSiteURL(t *testing.T) {
	svcWithDomain := func(project, service, host string, tls bool) seed {
		s := &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{Name: project + "-" + service, Namespace: "kuso"},
			Spec: kube.KusoServiceSpec{
				Project: project,
				Domains: []kube.KusoDomain{{Host: host, TLS: tls}},
			},
		}
		return typedSeed(kube.GVRServices, "KusoService", s)
	}
	prodEnv := func(project, service, host string, tlsHosts []string, internal bool) seed {
		e := &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      project + "-" + service + "-production",
				Namespace: "kuso",
				Labels: map[string]string{
					kube.LabelProject: project,
					kube.LabelService: service,
					kube.LabelEnv:     "production",
				},
			},
			Spec: kube.KusoEnvironmentSpec{
				Project: project, Service: project + "-" + service, Kind: "production",
				Host: host, TLSHosts: tlsHosts, Internal: internal,
			},
		}
		return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
	}

	t.Run("explicit service domain wins", func(t *testing.T) {
		s := fakeService(t,
			svcWithDomain("distill", "web", "custom.example.com", true),
			prodEnv("distill", "web", "web.distill.sislelabs.com", []string{"web.distill.sislelabs.com"}, false),
		)
		got := lookupSiteURL(context.Background(), s.Kube, "kuso", "distill", "distill-web")
		if got != "https://custom.example.com" {
			t.Errorf("got %q, want the explicit service domain", got)
		}
	})

	t.Run("falls back to production env auto-host (TLS)", func(t *testing.T) {
		// Service has NO spec.domains — the scaffold case.
		s := fakeService(t,
			seedService("scaffold", "scaffold"),
			prodEnv("scaffold", "scaffold", "scaffold.scaffold.sislelabs.com",
				[]string{"scaffold.scaffold.sislelabs.com"}, false),
		)
		got := lookupSiteURL(context.Background(), s.Kube, "kuso", "scaffold", "scaffold-scaffold")
		if got != "https://scaffold.scaffold.sislelabs.com" {
			t.Errorf("got %q, want the production env https host", got)
		}
	})

	t.Run("http when host not in TLSHosts", func(t *testing.T) {
		s := fakeService(t,
			seedService("p", "s"),
			prodEnv("p", "s", "s.p.example.com", nil, false),
		)
		got := lookupSiteURL(context.Background(), s.Kube, "kuso", "p", "p-s")
		if got != "http://s.p.example.com" {
			t.Errorf("got %q, want http (host not TLS-eligible)", got)
		}
	})

	t.Run("internal-only service has no public link", func(t *testing.T) {
		s := fakeService(t,
			seedService("p", "worker"),
			prodEnv("p", "worker", "worker.p.example.com", []string{"worker.p.example.com"}, true),
		)
		got := lookupSiteURL(context.Background(), s.Kube, "kuso", "p", "p-worker")
		if got != "" {
			t.Errorf("got %q, want empty for internal-only service", got)
		}
	})

	t.Run("no env, no domain -> empty", func(t *testing.T) {
		s := fakeService(t, seedService("p", "s"))
		got := lookupSiteURL(context.Background(), s.Kube, "kuso", "p", "p-s")
		if got != "" {
			t.Errorf("got %q, want empty when nothing resolves", got)
		}
	})
}

// TestBuildRichCard_NoSiteURLOnFailure verifies the Site field is NOT
// added for failed/cancelled/superseded builds — clicking through to
// "the live site" of a failed build would land on the prior version,
// which is misleading.
func TestBuildRichCard_NoSiteURLOnFailure(t *testing.T) {
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{Project: "p", Service: "p-s"}}
	for _, phase := range []string{"failed", "cancelled", "superseded"} {
		t.Run(phase, func(t *testing.T) {
			_, _, fields := buildRichCard(b, "s", phase, "boom", "https://example.com")
			for _, f := range fields {
				if f.Name == "Site" {
					t.Errorf("Site field leaked into %s build: %+v", phase, f)
				}
			}
		})
	}
}

// TestIsHexSHA covers the heuristic used to discriminate a real (short
// or full) git SHA from a synthetic redeploy ref.
func TestIsHexSHA(t *testing.T) {
	cases := map[string]bool{
		"":             false,
		"ab12":         false, // too short
		"abcdef0":      true,  // 7-char short SHA (git's default abbrev)
		"53d3f34262ef": true,  // 12-char short SHA
		"main-mp81chv": false, // synthetic ref shape
		"BADBEEF":      false, // uppercase rejected (git outputs lowercase)
		"zzz1234":      false, // out-of-range hex
	}
	for in, want := range cases {
		if got := isHexSHA(in); got != want {
			t.Errorf("isHexSHA(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestBuildRichCard_NoData covers an emit where the CR has no commit
// message and no start time — the card still renders with at minimum
// the title and (if present) ref.
func TestBuildRichCard_NoData(t *testing.T) {
	b := &kube.KusoBuild{
		Spec: kube.KusoBuildSpec{
			Project: "p",
			Service: "p-s",
		},
	}
	title, desc, fields := buildRichCard(b, "s", "succeeded", "", "")
	if title != "✓ Build succeeded · p / s" {
		t.Errorf("title: %q", title)
	}
	if desc != "" {
		t.Errorf("description should be empty: %q", desc)
	}
	if len(fields) != 0 {
		t.Errorf("expected no fields, got %+v", fields)
	}
}

// TestFormatBuildDuration pins the human-readable duration format.
// The values are what users see in the Discord card; changing them
// is a UI change, not a refactor.
func TestFormatBuildDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{1_000, "1s"},
		{59_000, "59s"},
		{60_000, "1m"},
		{84_000, "1m 24s"},
		{3_600_000, "1h"},
		{3_900_000, "1h 5m"},
		{7_200_000, "2h"},
	}
	for _, tc := range tests {
		if got := formatBuildDuration(tc.ms); got != tc.want {
			t.Errorf("formatBuildDuration(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

// TestBuildDurationMs handles the failure modes: missing stamps,
// malformed times, end-before-start. All return 0 so the field drops
// out of the card.
func TestBuildDurationMs(t *testing.T) {
	tests := []struct {
		name  string
		annos map[string]string
		want  int64
	}{
		{"both present", map[string]string{
			annStartedAt:   "2026-05-16T12:00:00Z",
			annCompletedAt: "2026-05-16T12:00:42Z",
		}, 42_000},
		{"missing start", map[string]string{
			annCompletedAt: "2026-05-16T12:00:42Z",
		}, 0},
		{"missing end", map[string]string{
			annStartedAt: "2026-05-16T12:00:00Z",
		}, 0},
		{"malformed", map[string]string{
			annStartedAt:   "not-a-time",
			annCompletedAt: "2026-05-16T12:00:42Z",
		}, 0},
		{"end before start (clock skew)", map[string]string{
			annStartedAt:   "2026-05-16T12:00:42Z",
			annCompletedAt: "2026-05-16T12:00:00Z",
		}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &kube.KusoBuild{ObjectMeta: metav1.ObjectMeta{Annotations: tc.annos}}
			if got := buildDurationMs(b); got != tc.want {
				t.Errorf("buildDurationMs() = %d, want %d", got, tc.want)
			}
		})
	}
}

// seedServiceWithDisplay seeds a KusoService carrying a cosmetic
// displayName, for the notification-label tests.
func seedServiceWithDisplay(project, service, displayName string) seed {
	s := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: project + "-" + service, Namespace: "kuso"},
		Spec:       kube.KusoServiceSpec{Project: project, DisplayName: displayName},
	}
	return typedSeed(kube.GVRServices, "KusoService", s)
}

// TestServiceDisplayLabel covers the notification title naming: use the
// service's cosmetic displayName when set, fall back to the slug
// otherwise (and on any kube/lookup miss).
func TestServiceDisplayLabel(t *testing.T) {
	t.Parallel()
	svc := fakeService(t,
		seedServiceWithDisplay("alpha", "web", "payload"),
		seedService("beta", "api"), // no displayName
	)
	cases := []struct {
		name       string
		fqn, short string
		want       string
	}{
		{"displayName set → used", "alpha-web", "web", "payload"},
		{"no displayName → slug", "beta-api", "api", "api"},
		{"unknown service → slug fallback", "ghost-x", "x", "x"},
	}
	for _, c := range cases {
		got := serviceDisplayLabel(context.Background(), svc.Kube, "kuso", c.fqn, c.short)
		if got != c.want {
			t.Errorf("%s: serviceDisplayLabel(%q,%q) = %q, want %q", c.name, c.fqn, c.short, got, c.want)
		}
	}
	// nil kube client → always the slug, never a panic.
	if got := serviceDisplayLabel(context.Background(), nil, "kuso", "alpha-web", "web"); got != "web" {
		t.Errorf("nil kube: got %q, want %q", got, "web")
	}
}
