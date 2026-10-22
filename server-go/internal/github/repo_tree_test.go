package github

import (
	"strings"
	"testing"
)

func TestDetectFromFiles_DotNet(t *testing.T) {
	noRead := func(string) string { return "" }
	cases := []struct {
		name        string
		files       []string
		wantRuntime string
		wantPort    int
	}{
		{"csproj at root", []string{"Program.cs", "MyApp.csproj"}, "buildpacks", 8080},
		// MVC templates often ship a package.json for front-end tooling.
		{"csproj with package.json", []string{"package.json", "Web.csproj"}, "buildpacks", 8080},
		{"Dockerfile wins over csproj", []string{"Dockerfile", "MyApp.csproj"}, "dockerfile", 8080},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectFromFiles(tc.files, noRead)
			if got.Runtime != tc.wantRuntime || got.Port != tc.wantPort {
				t.Fatalf("got runtime=%s port=%d (%s), want runtime=%s port=%d",
					got.Runtime, got.Port, got.Reason, tc.wantRuntime, tc.wantPort)
			}
		})
	}
}

// A solution layout can't build from the repo root; the reason has to
// tell the caller which --path to use instead.
func TestDetectFromFiles_DotNetInSubdirPointsAtPath(t *testing.T) {
	got := DetectFromFiles([]string{"MyApp.sln", "MyApp/MyApp.csproj", "MyApp/Program.cs"}, func(string) string { return "" })
	if got.Runtime != "nixpacks" || !strings.Contains(got.Reason, "--path MyApp") {
		t.Fatalf("got runtime=%s reason=%q, want nixpacks fallback with a --path MyApp hint", got.Runtime, got.Reason)
	}
}
