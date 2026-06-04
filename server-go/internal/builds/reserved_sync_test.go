package builds

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestReservedKeysMatchRenderScript guards the invariant that the Go
// reservedBuildEnvKeys map stays in lockstep with the RESERVED= shell
// strings in buildcontroller/render.go. The two are independent copies of
// the same filter (one drops keys server-side before they reach
// spec.buildEnv; the other is defense-in-depth inside the build script).
// They previously diverged — render.go listed NODE_ENV but the map didn't —
// which let NODE_ENV=production poison builds. This test parses the actual
// RESERVED= literals out of render.go and asserts every one equals the map,
// so hand-editing one copy and forgetting the other fails CI instead of
// shipping a silent gap.
func TestReservedKeysMatchRenderScript(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("../buildcontroller/render.go")
	if err != nil {
		t.Fatalf("read render.go: %v", err)
	}
	re := regexp.MustCompile(`RESERVED="([^"]*)"`)
	matches := re.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("no RESERVED= strings found in render.go — did the script change shape?")
	}

	want := make([]string, 0, len(reservedBuildEnvKeys))
	for k := range reservedBuildEnvKeys {
		want = append(want, k)
	}
	sort.Strings(want)
	wantSet := strings.Join(want, " ")

	for i, m := range matches {
		got := strings.Fields(m[1])
		sort.Strings(got)
		gotSet := strings.Join(got, " ")
		if gotSet != wantSet {
			t.Errorf("RESERVED string #%d in render.go diverges from reservedBuildEnvKeys.\n  render.go: %s\n  go map:    %s\nKeep the two in lockstep (see buildenv.go).", i+1, gotSet, wantSet)
		}
	}
}
