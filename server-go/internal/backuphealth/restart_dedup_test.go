package backuphealth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The watcher edge-triggers: it emits only when the SET of unhealthy
// subsystems CHANGES. That state lived purely in memory, so every
// kuso-server restart reset it and a condition that had been unhealthy
// for hours re-fired as though it were new.
//
// Observed: three identical "Addon backups failing / sportnopz-storage"
// alerts in one afternoon, one per release (v0.26.3, v0.26.4, v0.26.5),
// for a single unchanged failure. Each rollout restarts the server, so
// shipping fixes produced a burst of pages about the bug being fixed.
//
// shouldEmit is the pure decision, split out so the restart case can be
// tested without a kube client.
func TestShouldEmit_SuppressesUnchangedStateAcrossRestart(t *testing.T) {
	cases := []struct {
		name      string
		persisted string // what a previous process recorded
		evaluated bool   // false = this process just started
		current   string
		want      bool
		why       string
	}{
		{
			name: "first ever evaluation, unhealthy", persisted: "", evaluated: false,
			current: "addon|warning", want: true,
			why: "a cold start that is already broken must alert",
		},
		{
			name: "restart, SAME unhealthy state as before", persisted: "addon|warning", evaluated: false,
			current: "addon|warning", want: false,
			why: "THE BUG: re-firing an unchanged condition on every deploy",
		},
		{
			name: "restart, state ESCALATED while we were down", persisted: "addon|warning", evaluated: false,
			current: "addon|error", want: true,
			why: "severity is part of the state; an escalation must fire",
		},
		{
			name: "restart, a NEW subsystem broke while we were down", persisted: "addon|warning", evaluated: false,
			current: "addon,backup|warning", want: true,
			why: "the set changed",
		},
		{
			name: "restart, everything recovered while we were down", persisted: "addon|warning", evaluated: false,
			current: "", want: true,
			why: "the recovery event must still fire",
		},
		{
			name: "steady state within one process, unchanged", persisted: "addon|warning", evaluated: true,
			current: "addon|warning", want: false,
			why: "ordinary edge-trigger suppression",
		},
		{
			name: "healthy before and after", persisted: "", evaluated: true,
			current: "", want: false,
			why: "nothing to say",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldEmit(tc.evaluated, tc.persisted, tc.current); got != tc.want {
				t.Fatalf("shouldEmit(evaluated=%v, persisted=%q, current=%q) = %v, want %v (%s)",
					tc.evaluated, tc.persisted, tc.current, got, tc.want, tc.why)
			}
		})
	}
}

// shouldEmit is only half the fix: the decision is correct, but it can
// only suppress a restart re-fire if lastState is SEEDED from the
// previous process. Removing the loadState call compiles and leaves
// every decision test green while restoring the original bug, so
// assert on the call graph too.
func TestRunSeedsStateFromPersistedAnnotation(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "backuphealth.go", nil, 0)
	if err != nil {
		t.Fatalf("parse backuphealth.go: %v", err)
	}

	var run, tick *ast.FuncDecl
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fn.Name.Name {
		case "Run":
			run = fn
		case "tick":
			tick = fn
		}
	}
	if run == nil {
		t.Fatal("Run not found — renamed? update this guard")
	}

	calls := func(fn *ast.FuncDecl, name string) bool {
		if fn == nil {
			return false
		}
		found := false
		ast.Inspect(fn, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
					found = true
				}
			}
			return true
		})
		return found
	}

	if !calls(run, "loadState") {
		t.Error("Run never calls loadState — lastState starts empty on every " +
			"restart, so an unchanged failure re-fires on every deploy")
	}
	// saveState lives in tick (where the state actually changes), not Run.
	if !calls(run, "saveState") && !calls(tick, "saveState") {
		t.Error("nothing calls saveState — the state is never persisted, so " +
			"loadState always returns empty and the fix is inert")
	}
}
