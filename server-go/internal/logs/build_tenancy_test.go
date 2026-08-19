package logs

import (
	"context"
	"errors"
	"testing"

	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// fakeBuildLogs is a BuildLogReader whose archive rows are keyed by
// build name, mirroring the BuildLog table.
type fakeBuildLogs struct {
	owner map[string]string // buildName -> owning project
	logs  map[string]string // buildName -> archived tail
	err   error             // when set, both methods fail
}

func (f fakeBuildLogs) GetBuildLog(_ context.Context, project, buildName string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	// Mirrors the real query's WHERE buildName=$1 AND project=$2.
	if f.owner[buildName] != project {
		return "", nil
	}
	return f.logs[buildName], nil
}

func (f fakeBuildLogs) BuildLogProject(_ context.Context, buildName string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.owner[buildName], nil
}

// Build names are the predictable "<project>-<service>-<ref>" string
// (builds.BuildName), so a caller authorized on ANY project can guess a
// foreign build's name. Ownership must be proven, not assumed from the
// name or from namespace scoping — projects without a custom
// spec.namespace all share the home namespace on a default install.
//
// Kube is nil here, which exercises the archive-oracle path: the case
// where the KusoBuild CR has already been reaped by retention and the
// archive row is the only remaining record of ownership.
func TestBuildBelongsToProject_ArchiveOracle(t *testing.T) {
	archive := fakeBuildLogs{
		owner: map[string]string{
			"victim-api-abc123":  "victim",
			"attacker-web-def45": "attacker",
		},
		logs: map[string]string{
			"victim-api-abc123":  "DATABASE_URL=postgres://secret",
			"attacker-web-def45": "ok",
		},
	}
	s := &Service{Namespace: "kuso", BuildLogs: archive}

	cases := []struct {
		name      string
		project   string
		buildName string
		want      bool
	}{
		{"owner reads own build", "attacker", "attacker-web-def45", true},
		{"victim reads own build", "victim", "victim-api-abc123", true},

		// The vulnerability: authorized on "attacker", naming a build
		// owned by "victim".
		{"cross-project read is denied", "attacker", "victim-api-abc123", false},

		{"unknown build denied", "attacker", "does-not-exist", false},
		{"empty project denied", "", "victim-api-abc123", false},
		{"empty build denied", "attacker", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.buildBelongsToProject(context.Background(), "kuso", tc.buildName, tc.project)
			if got != tc.want {
				t.Errorf("buildBelongsToProject(project=%q, build=%q) = %v, want %v",
					tc.project, tc.buildName, got, tc.want)
			}
		})
	}
}

// A DB error must not become an authorization pass.
func TestBuildBelongsToProject_FailsClosedOnError(t *testing.T) {
	s := &Service{
		Namespace: "kuso",
		BuildLogs: fakeBuildLogs{err: errors.New("postgres unreachable")},
	}
	if s.buildBelongsToProject(context.Background(), "kuso", "victim-api-abc123", "attacker") {
		t.Error("archive lookup error must deny access, not grant it")
	}
	// Even the legitimate owner is denied while ownership is unprovable —
	// failing closed is the correct trade here.
	if s.buildBelongsToProject(context.Background(), "kuso", "victim-api-abc123", "victim") {
		t.Error("ownership must be positively proven; an errored lookup grants nothing")
	}
}

// With neither a kube client nor an archive there is no oracle at all,
// so every request must be denied rather than falling through.
func TestBuildBelongsToProject_NoSourcesDenies(t *testing.T) {
	s := &Service{Namespace: "kuso"}
	if s.buildBelongsToProject(context.Background(), "kuso", "victim-api-abc123", "victim") {
		t.Error("no ownership source available must deny")
	}
}

// The archive query itself must be project-scoped, so that even a
// caller who somehow reaches GetBuildLog cannot read another tenant's
// tail. This pins the WHERE ... AND "project"=$2 predicate.
func TestGetBuildLogIsProjectScoped(t *testing.T) {
	archive := fakeBuildLogs{
		owner: map[string]string{"victim-api-abc123": "victim"},
		logs:  map[string]string{"victim-api-abc123": "DATABASE_URL=postgres://secret"},
	}
	got, err := archive.GetBuildLog(context.Background(), "attacker", "victim-api-abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("cross-project archive read returned %q, want empty", got)
	}
	if got, _ := archive.GetBuildLog(context.Background(), "victim", "victim-api-abc123"); got == "" {
		t.Error("owner must still be able to read their own archived tail")
	}
}

// --- Call-site regression guards -------------------------------------
//
// The guard function above was always correct. The bug was that the REST
// Tail path never CALLED it, while the streaming WS path did — so a
// viewer authorized on project A could read project B's build output via
//
//	GET /api/projects/A/services/x/logs?env=build:<B's build>
//
// These tests pin the call sites, not the predicate, because that is
// where the divergence lived. Kube is nil so the archive row is the sole
// ownership oracle; a Tail that skipped the check would fall through to
// the pod lookup / archive fetch and return content instead of erroring.

func TestTail_BuildEnv_DeniesForeignProject(t *testing.T) {
	s := &Service{BuildLogs: fakeBuildLogs{
		owner: map[string]string{"victim-api-abc123": "victim"},
		logs:  map[string]string{"victim-api-abc123": "SECRET build output\n"},
	}}

	_, _, err := s.Tail(context.Background(), "attacker", "api", "build:victim-api-abc123", 100)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Tail(project=attacker, env=build:victim-...) err = %v, want ErrNotFound "+
			"— a foreign build's logs must not be readable cross-project", err)
	}
}

func TestTail_BuildEnv_AllowsOwningProject(t *testing.T) {
	// A kube client with no pods: tailBuildPods finds nothing and Tail
	// falls through to the archived tail, which is the path this test
	// asserts. (tailBuildPods dereferences s.Kube unconditionally, so a
	// nil Kube would panic before reaching the fallback.)
	s := &Service{
		Kube: &kube.Client{Clientset: kubefake.NewSimpleClientset()},
		BuildLogs: fakeBuildLogs{
			owner: map[string]string{"victim-api-abc123": "victim"},
			logs:  map[string]string{"victim-api-abc123": "line one\nline two\n"},
		},
	}

	out, _, err := s.Tail(context.Background(), "victim", "api", "build:victim-api-abc123", 100)
	if err != nil {
		t.Fatalf("Tail for the OWNING project err = %v, want nil (the fix must not break the legitimate read)", err)
	}
	if len(out) == 0 {
		t.Fatal("Tail for the owning project returned no lines; the archive fallback should have served them")
	}
}

// The run: branch had the identical gap — and unlike builds, NEITHER the
// REST nor the WS path checked ownership. Run pods execute migrations and
// one-off tasks with the service's full resolved env, so their output is
// at least as sensitive as a build's. Kube is nil here, which is itself
// the denial path: runBelongsToProject cannot prove ownership without a
// readable KusoRun CR, and fails closed.
func TestTail_RunEnv_FailsClosedWithoutOwnershipProof(t *testing.T) {
	s := &Service{}

	_, _, err := s.Tail(context.Background(), "attacker", "api", "run:victim-api-migrate", 100)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Tail(env=run:...) err = %v, want ErrNotFound when ownership can't be proven", err)
	}
}

func TestRunBelongsToProject_FailsClosedOnNilKube(t *testing.T) {
	s := &Service{}
	if s.runBelongsToProject(context.Background(), "kuso", "victim-api-migrate", "attacker") {
		t.Error("runBelongsToProject with no kube client returned true; must fail closed")
	}
	if s.runBelongsToProject(context.Background(), "kuso", "", "victim") {
		t.Error("runBelongsToProject with empty run name returned true; must fail closed")
	}
}
