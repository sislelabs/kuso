package logs

import (
	"context"
	"errors"
	"testing"
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
