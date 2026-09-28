package builds

import (
	"context"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/failures"
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
	title, desc, fields := buildRichCard(b, "web", "succeeded", "", nil)
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
	title, desc, fields := buildRichCard(b, "web", "failed", "kaniko: COPY failed: not found", nil)
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
	_, desc, fields := buildRichCard(b, "web", "succeeded", "", nil)
	if desc != "Manual redeploy of `main` by ivo9999" {
		t.Errorf("synthetic-ref description: %q", desc)
	}
	// Ref field shows only the branch, not the synth suffix.
	if fields[0].Name != "Ref" || fields[0].Value != "`main`" {
		t.Errorf("synth-ref field should hide the suffix: %+v", fields[0])
	}
}

// TestLookupBuildTargets covers env resolution for the build card: only
// envs the build's branch promotes into, each with its OWN host. The
// regression: a build of a feature branch tracked by a custom "verify" env
// linked the production host and never named the env.
func TestLookupBuildTargets(t *testing.T) {
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
	env := func(project, service, group, branch, host string, tlsHosts []string, internal bool) seed {
		e := &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      project + "-" + service + "-" + group,
				Namespace: "kuso",
				Labels: map[string]string{
					kube.LabelProject: project,
					kube.LabelService: service,
					kube.LabelEnv:     group,
				},
			},
			Spec: kube.KusoEnvironmentSpec{
				Project: project, Service: project + "-" + service, Kind: "production",
				Branch: branch, Host: host, TLSHosts: tlsHosts, Internal: internal,
			},
		}
		return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
	}
	build := func(project, service, branch string) *kube.KusoBuild {
		return &kube.KusoBuild{Spec: kube.KusoBuildSpec{Project: project, Service: project + "-" + service, Branch: branch}}
	}
	lookup := func(s *Service, b *kube.KusoBuild) []buildTarget {
		return lookupBuildTargets(context.Background(), s.Kube, "kuso", "kuso", b)
	}

	t.Run("feature-branch build targets its own env, not production", func(t *testing.T) {
		s := fakeService(t,
			seedService("scubatony", "internal-system"),
			env("scubatony", "internal-system", "production", "main",
				"internal-system.scubatony.sislelabs.com", []string{"internal-system.scubatony.sislelabs.com"}, false),
			env("scubatony", "internal-system", "staging", "staging",
				"internal-system-staging.scubatony.sislelabs.com", []string{"internal-system-staging.scubatony.sislelabs.com"}, false),
			env("scubatony", "internal-system", "verify", "bugfix/verified-fixes-2026-09",
				"internal-system-verify.scubatony.sislelabs.com", []string{"internal-system-verify.scubatony.sislelabs.com"}, false),
		)
		got := lookup(s, build("scubatony", "internal-system", "bugfix/verified-fixes-2026-09"))
		want := []buildTarget{{Env: "verify", URL: "https://internal-system-verify.scubatony.sislelabs.com"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("custom service domain applies to production only", func(t *testing.T) {
		s := fakeService(t,
			svcWithDomain("distill", "web", "custom.example.com", true),
			env("distill", "web", "production", "main", "web.distill.sislelabs.com", []string{"web.distill.sislelabs.com"}, false),
			env("distill", "web", "staging", "staging", "web-staging.distill.sislelabs.com", nil, false),
		)
		if got := lookup(s, build("distill", "web", "main")); !reflect.DeepEqual(got, []buildTarget{{Env: "production", URL: "https://custom.example.com"}}) {
			t.Errorf("main: got %+v", got)
		}
		if got := lookup(s, build("distill", "web", "staging")); !reflect.DeepEqual(got, []buildTarget{{Env: "staging", URL: "http://web-staging.distill.sislelabs.com"}}) {
			t.Errorf("staging: got %+v", got)
		}
	})

	t.Run("internal-only env is named but has no link", func(t *testing.T) {
		s := fakeService(t,
			seedService("p", "worker"),
			env("p", "worker", "production", "main", "worker.p.example.com", []string{"worker.p.example.com"}, true),
		)
		got := lookup(s, build("p", "worker", "main"))
		if !reflect.DeepEqual(got, []buildTarget{{Env: "production"}}) {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("dry run and unmatched branch have no targets", func(t *testing.T) {
		s := fakeService(t,
			seedService("p", "s"),
			env("p", "s", "production", "main", "s.p.example.com", nil, false),
		)
		dry := build("p", "s", "main")
		dry.Spec.DryRun = true
		if got := lookup(s, dry); got != nil {
			t.Errorf("dry run: got %+v", got)
		}
		if got := lookup(s, build("p", "s", "nobody-tracks-this")); got != nil {
			t.Errorf("unmatched: got %+v", got)
		}
	})
}

// TestBuildRichCard_TargetsInTitle checks the card names the env(s),
// and that site URLs no longer ride in a field (they're link-row items).
func TestBuildRichCard_TargetsInTitle(t *testing.T) {
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{Project: "scubatony", Service: "scubatony-internal-system"}}
	targets := []buildTarget{
		{Env: "production", URL: "https://a.example.com"},
		{Env: "worker-env"},
		{Env: "verify", URL: "https://b.example.com"},
	}
	for _, phase := range []string{"succeeded", "failed", "cancelled", "superseded"} {
		title, _, fields := buildRichCard(b, "internal-system", phase, "boom", targets)
		if !strings.HasSuffix(title, " · scubatony / internal-system → production, worker-env, verify") {
			t.Errorf("%s title: %q", phase, title)
		}
		for _, f := range fields {
			if strings.Contains(f.Value, "example.com") {
				t.Errorf("%s: site URL leaked into field %+v", phase, f)
			}
		}
	}
}

func TestBuildCardLinks(t *testing.T) {
	base := "/projects/p?service=s"
	one := []buildTarget{{Env: "staging", URL: "https://s-staging.example.com"}}
	two := []buildTarget{{Env: "production", URL: "https://s.example.com"}, {Env: "internal"}}
	cls := &failures.Classification{Kind: "build_oom", Tab: failures.TabBuild}
	cases := []struct {
		name    string
		phase   string
		targets []buildTarget
		c       *failures.Classification
		want    []EnvelopeLink
	}{
		{"failed, one env", "failed", one, cls, []EnvelopeLink{
			{"View failure", base + "&tab=deployments&kind=build_oom&env=staging"},
		}},
		{"failed, no classification", "failed", nil, nil, []EnvelopeLink{
			{"View failure", base + "&tab=deployments"},
		}},
		{"succeeded opens every env with a URL", "succeeded", two, nil, []EnvelopeLink{
			{"Deployments", base + "&tab=deployments"},
			{"Open production", "https://s.example.com"},
		}},
		{"succeeded, one env", "succeeded", one, nil, []EnvelopeLink{
			{"Deployments", base + "&tab=deployments&env=staging"},
			{"Open staging", "https://s-staging.example.com"},
		}},
		{"cancelled", "cancelled", two, nil, []EnvelopeLink{{"Deployments", base + "&tab=deployments"}}},
		{"superseded", "superseded", one, nil, []EnvelopeLink{{"Deployments", base + "&tab=deployments&env=staging"}}},
		{"release failed", "release-failed", []buildTarget{{Env: "production"}}, nil, []EnvelopeLink{
			{"Deployments", base + "&tab=deployments&env=production"},
		}},
	}
	for _, tc := range cases {
		if got := buildCardLinks("p", "s", tc.phase, tc.targets, tc.c); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
	if got := buildCardLinks("", "s", "failed", nil, nil); got != nil {
		t.Errorf("no project: %+v", got)
	}
}

func TestBuildSeverity(t *testing.T) {
	cases := []struct {
		name    string
		targets []buildTarget
		want    string
	}{
		{"unknown targets", nil, "error"},
		{"production", []buildTarget{{Env: "production"}}, "error"},
		{"production among others", []buildTarget{{Env: "staging"}, {Env: "production"}}, "error"},
		{"unnamed env", []buildTarget{{Env: ""}}, "error"},
		{"staging", []buildTarget{{Env: "staging"}}, "warn"},
		{"preview + custom", []buildTarget{{Env: "preview-pr-7"}, {Env: "verify"}}, "warn"},
	}
	for _, tc := range cases {
		if got := buildSeverity(tc.targets); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBuildRichCard_RefLinks(t *testing.T) {
	const sha = "9f5792fabc0123456789abcdef0123456789abcd"
	card := func(repoURL, branch, ref string) string {
		b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
			Project: "p", Service: "p-s", Branch: branch, Ref: ref,
			Repo: &kube.KusoRepoRef{URL: repoURL},
		}}
		_, _, fields := buildRichCard(b, "s", "succeeded", "", nil)
		if len(fields) == 0 || fields[0].Name != "Ref" {
			t.Fatalf("no Ref field: %+v", fields)
		}
		return fields[0].Value
	}
	cases := []struct {
		name, repo, branch, ref, want string
	}{
		{"github https", "https://github.com/o/r", "main", sha,
			"[`main`](https://github.com/o/r/tree/main) · [`9f5792f`](https://github.com/o/r/commit/" + sha + ")"},
		{"github .git suffix", "https://github.com/o/r.git", "main", sha,
			"[`main`](https://github.com/o/r/tree/main) · [`9f5792f`](https://github.com/o/r/commit/" + sha + ")"},
		{"github ssh", "git@github.com:o/r.git", "main", sha,
			"[`main`](https://github.com/o/r/tree/main) · [`9f5792f`](https://github.com/o/r/commit/" + sha + ")"},
		{"slashed branch keeps slashes, escapes segments", "https://github.com/o/r", "feat/a b#1", sha,
			"[`feat/a b#1`](https://github.com/o/r/tree/feat/a%20b%231) · [`9f5792f`](https://github.com/o/r/commit/" + sha + ")"},
		{"gitlab", "https://gitlab.com/g/sub/r.git", "main", sha,
			"[`main`](https://gitlab.com/g/sub/r/-/tree/main) · [`9f5792f`](https://gitlab.com/g/sub/r/-/commit/" + sha + ")"},
		{"unknown host stays plain", "https://git.example.com/o/r", "main", sha, "`main` · `9f5792f`"},
		{"no repo stays plain", "", "main", sha, "`main` · `9f5792f`"},
		{"synthetic ref: branch link, no commit", "https://github.com/o/r", "main", "main-mp81chv5",
			"[`main`](https://github.com/o/r/tree/main)"},
	}
	for _, tc := range cases {
		if got := card(tc.repo, tc.branch, tc.ref); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

func TestReplacedByDescription(t *testing.T) {
	if got := replacedByDescription("main", "9f5792fabc01"); got != "Replaced by a newer build (`9f5792f`)" {
		t.Errorf("sha: %q", got)
	}
	if got := replacedByDescription("main", "main-mp81chv5"); got != "Replaced by a newer build of `main`" {
		t.Errorf("synthetic: %q", got)
	}
	if got := replacedByDescription("", ""); got != "Replaced by a newer build" {
		t.Errorf("empty: %q", got)
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
	title, desc, fields := buildRichCard(b, "s", "succeeded", "", nil)
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

// TestBuildRichCard_ReasonAlongsideCommitMessage: a failed push build has
// a commit message, which takes the description slot. The reason must
// still reach the card as a field, since renderers ignore Body once a
// Description is set.
func TestBuildRichCard_ReasonAlongsideCommitMessage(t *testing.T) {
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{annCommitMessage: "fix: thing"}},
		Spec:       kube.KusoBuildSpec{Project: "p", Service: "p-s", Branch: "main", Ref: "abcdef1234"},
	}
	for _, phase := range []string{"failed", "cancelled"} {
		_, desc, fields := buildRichCard(b, "s", phase, "build timed out after 30m", nil)
		if desc != "fix: thing" {
			t.Errorf("%s: description %q", phase, desc)
		}
		var reason string
		for _, f := range fields {
			if f.Name == "Reason" {
				reason = f.Value
			}
		}
		if reason != "build timed out after 30m" {
			t.Errorf("%s: Reason field %q (fields %+v)", phase, reason, fields)
		}
	}
	// Without a commit message the reason IS the description; no duplicate field.
	b.Annotations = nil
	_, desc, fields := buildRichCard(b, "s", "failed", "boom", nil)
	for _, f := range fields {
		if f.Name == "Reason" {
			t.Errorf("duplicate Reason field when description already carries it: %+v", fields)
		}
	}
	if desc != "boom" {
		t.Errorf("description %q", desc)
	}
}

func TestWithEnvParam(t *testing.T) {
	base := "/projects/p?service=s"
	if got := withEnvParam(base, []buildTarget{{Env: "preview-pr-7"}}); got != base+"&env=preview-pr-7" {
		t.Errorf("single target: %q", got)
	}
	if got := withEnvParam(base, []buildTarget{{Env: "production"}, {Env: "staging"}}); got != base {
		t.Errorf("multi target must not pin an env: %q", got)
	}
	if got := withEnvParam("", []buildTarget{{Env: "x"}}); got != "" {
		t.Errorf("empty url: %q", got)
	}
}
