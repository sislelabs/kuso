package failures

import (
	"testing"
)

func TestClassify_PodStatusSignals(t *testing.T) {
	tests := []struct {
		name     string
		sig      Signal
		wantKind Kind
		wantTab  Tab
	}{
		{"OOMKilled reason", Signal{Reason: "OOMKilled"}, KindOOM, TabLogs},
		{"exit 137 no reason", Signal{ExitCode: 137}, KindOOM, TabLogs},
		{"CrashLoopBackOff", Signal{Reason: "CrashLoopBackOff"}, KindCrashLoop, TabLogs},
		{"ErrImagePull", Signal{Reason: "ErrImagePull"}, KindImagePullFailed, TabSettings},
		{"ImagePullBackOff", Signal{Reason: "ImagePullBackOff"}, KindImagePullFailed, TabSettings},
		{"CreateContainerConfigError", Signal{Reason: "CreateContainerConfigError"}, KindMissingEnv, TabVariables},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(nil, tc.sig)
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Tab != tc.wantTab {
				t.Errorf("Tab = %q, want %q", got.Tab, tc.wantTab)
			}
			if got.Summary == "" {
				t.Error("Summary is empty")
			}
		})
	}
}

func TestClassify_LogRegexes(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		wantKind Kind
		wantTab  Tab
		// substrMustAppearInSummary lets us assert the summary actually
		// extracted the salient detail (env name / port number) without
		// hard-coding the whole sentence.
		substrMustAppearInSummary string
	}{
		{
			"missing env DATABASE_URL",
			[]string{"npm install completed", "Error: Missing env var DATABASE_URL"},
			KindMissingEnv, TabVariables, "DATABASE_URL",
		},
		{
			// Some Python apps print a more verbose "environment variable
			// DATABASE_URL not set" before the KeyError; we match the
			// verbose form because a bare KeyError is too ambiguous (lots
			// of non-env KeyErrors exist).
			"python explicit env-var missing",
			[]string{"RuntimeError: environment variable DATABASE_URL not set"},
			KindMissingEnv, TabVariables, "DATABASE_URL",
		},
		{
			"port already in use 8080",
			[]string{"Error: listen EADDRINUSE: address already in use :::8080"},
			KindPortConflict, TabLogs, "8080",
		},
		{
			"readiness probe failed",
			[]string{"Warning  Unhealthy  pod/web  Readiness probe failed: HTTP probe failed with statuscode: 500"},
			KindHealthcheckFailed, TabLogs, "",
		},
		{
			"build failed",
			[]string{"error building image: failed to execute command"},
			KindBuildCommandFailed, TabLogs, "",
		},
		{
			"go panic",
			[]string{"panic: runtime error: invalid memory address or nil pointer dereference"},
			KindCrashLoop, TabLogs, "",
		},
		{
			"python traceback",
			[]string{"Traceback (most recent call last):"},
			KindCrashLoop, TabLogs, "",
		},
		{
			"no match falls back to generic",
			[]string{"some unrelated chatter", "more chatter"},
			KindGeneric, TabLogs, "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.lines, Signal{})
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q (summary=%q)", got.Kind, tc.wantKind, got.Summary)
			}
			if got.Tab != tc.wantTab {
				t.Errorf("Tab = %q, want %q", got.Tab, tc.wantTab)
			}
			if got.Summary == "" {
				t.Error("Summary is empty")
			}
			if tc.substrMustAppearInSummary != "" &&
				!containsAny(got.Summary, tc.substrMustAppearInSummary) {
				t.Errorf("Summary %q does not contain %q", got.Summary, tc.substrMustAppearInSummary)
			}
		})
	}
}

func TestClassify_SignalBeatsLogs(t *testing.T) {
	// An OOMKilled pod whose logs also mention "address already in use"
	// from an earlier boot still classifies as OOM.
	got := Classify(
		[]string{"Error: address already in use :::3000"},
		Signal{Reason: "OOMKilled"},
	)
	if got.Kind != KindOOM {
		t.Errorf("Kind = %q, want OOM (signal should beat log regex)", got.Kind)
	}
}

func TestClassify_RecentMostLineWins(t *testing.T) {
	// When two log detectors could match, the more-recent line (further
	// down the slice) wins — the failure that took the pod down was
	// the last thing that happened.
	got := Classify(
		[]string{
			"Missing env var DATABASE_URL",         // older
			"panic: runtime error: nil ptr deref", // newer
		},
		Signal{},
	)
	if got.Kind != KindCrashLoop {
		t.Errorf("Kind = %q, want CrashLoop (most recent line)", got.Kind)
	}
}

func TestClassify_LineHintTruncated(t *testing.T) {
	long := make([]byte, maxLineHint+200)
	for i := range long {
		long[i] = 'x'
	}
	line := "panic: " + string(long)
	got := Classify([]string{line}, Signal{})
	// The truncation appends a 3-byte UTF-8 ellipsis ("…"), so the
	// upper bound is maxLineHint + 3 bytes — not +1.
	if len(got.LineHint) > maxLineHint+3 {
		t.Errorf("LineHint not truncated: %d chars", len(got.LineHint))
	}
}

func TestClassify_LineNumIs1Based(t *testing.T) {
	got := Classify(
		[]string{"first line", "second line", "panic: bang"},
		Signal{},
	)
	if got.LineNum != 3 {
		t.Errorf("LineNum = %d, want 3 (1-based)", got.LineNum)
	}
	if got.LineHint == "" {
		t.Error("LineHint should be populated for log-matched kinds")
	}
}

func containsAny(haystack, needle string) bool {
	// Tiny helper — strings.Contains in 3 chars less.
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
