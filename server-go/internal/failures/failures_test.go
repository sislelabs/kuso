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

func TestClassify_BuildDetectors(t *testing.T) {
	tests := []struct {
		name       string
		lines      []string
		wantKind   Kind
		wantTab    Tab
		wantRemed  bool   // expect a non-nil Remediation
		summarySub string // optional substring the summary must contain
		fixSub     string // optional substring the Remediation.Fix must contain
	}{
		{
			name: "pnpm patch file missing from build context",
			lines: []string{
				"#14 [prod-deps 3/3] RUN pnpm install --frozen-lockfile",
				"#14 0.778  ENOENT  ENOENT: no such file or directory, open '/app/patches/@payloadcms__ui@3.85.1.patch'",
			},
			wantKind:   KindDockerfileMissingCopy,
			wantTab:    TabBuild,
			wantRemed:  true,
			summarySub: "@payloadcms__ui@3.85.1.patch",
			fixSub:     "COPY patches/",
		},
		{
			name:      "pnpm outdated lockfile",
			lines:     []string{"using pnpm@9.15.0", "ERR_PNPM_OUTDATED_LOCKFILE  Cannot install with \"frozen-lockfile\""},
			wantKind:  KindLockfileDrift,
			wantTab:   TabBuild,
			wantRemed: true,
			fixSub:    "pnpm install",
		},
		{
			name:      "npm ci lockfile out of sync",
			lines:     []string{"npm ci", "npm ERR! can only install packages when your package.json and package-lock.json are in sync"},
			wantKind:  KindLockfileDrift,
			wantTab:   TabBuild,
			wantRemed: true,
			fixSub:    "npm install",
		},
		{
			name:      "dockerfile not found",
			lines:     []string{"failed to read dockerfile: open Dockerfile: no such file or directory"},
			wantKind:  KindDockerfileNotFound,
			wantTab:   TabBuild,
			wantRemed: true,
		},
		{
			name:      "build OOM (node heap)",
			lines:     []string{"<--- Last few GCs --->", "FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory"},
			wantKind:  KindBuildOOM,
			wantTab:   TabBuild,
			wantRemed: true,
			fixSub:    "max-old-space-size",
		},
		{
			name:      "registry pull access denied",
			lines:     []string{"ERROR: failed to solve: pull access denied, repository does not exist or may require authorization"},
			wantKind:  KindRegistryAuth,
			wantTab:   TabBuild,
			wantRemed: true,
		},
		{
			name:      "dependency not resolvable",
			lines:     []string{"npm ERR! code ETARGET", "npm ERR! notarget No matching version found for left-pad@99.0.0"},
			wantKind:  KindDependencyResolution,
			wantTab:   TabBuild,
			wantRemed: true,
		},
		{
			// The clone init container was told to fetch a ref that no
			// longer exists (branch deleted / force-pushed while queued).
			name:      "clone couldn't find remote ref",
			lines:     []string{"Cloning into '/workspace/src'...", "fatal: couldn't find remote ref refs/heads/feature-x"},
			wantKind:  KindCloneRefMissing,
			wantTab:   TabLogs,
			wantRemed: true,
		},
		{
			name:      "clone remote branch not found",
			lines:     []string{"fatal: Remote branch pr-42 not found in upstream origin"},
			wantKind:  KindCloneRefMissing,
			wantTab:   TabLogs,
			wantRemed: true,
		},
		{
			// Ref-missing must WIN over a trailing generic "build failed"
			// line — otherwise the poller can't divert to cancelled.
			name:      "ref-missing beats trailing build-failed line",
			lines:     []string{"fatal: couldn't find remote ref refs/heads/gone", "error: build failed with exit code 128"},
			wantKind:  KindCloneRefMissing,
			wantTab:   TabLogs,
			wantRemed: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.lines, Signal{})
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Tab != tc.wantTab {
				t.Errorf("Tab = %q, want %q", got.Tab, tc.wantTab)
			}
			if tc.wantRemed && got.Remediation == nil {
				t.Fatalf("expected a Remediation, got nil")
			}
			if !tc.wantRemed && got.Remediation != nil {
				t.Errorf("expected no Remediation, got %+v", got.Remediation)
			}
			if tc.summarySub != "" && !containsAny(got.Summary, tc.summarySub) {
				t.Errorf("Summary %q does not contain %q", got.Summary, tc.summarySub)
			}
			if tc.fixSub != "" {
				if got.Remediation == nil || !containsAny(got.Remediation.Fix, tc.fixSub) {
					t.Errorf("Remediation.Fix does not contain %q (remed=%+v)", tc.fixSub, got.Remediation)
				}
			}
			if got.Remediation != nil && got.Remediation.Title == "" {
				t.Error("Remediation.Title is empty")
			}
		})
	}
}

// TestCloneRefMissing_DoesNotSwallowAuthFailures guards the narrowness of
// the clone-ref-missing detector: genuine clone failures (bad credentials,
// repo not found) must NOT be misclassified as ref-missing, or they'd be
// silently cancelled instead of failing loudly with their real cause.
func TestCloneRefMissing_DoesNotSwallowAuthFailures(t *testing.T) {
	authLines := [][]string{
		{"fatal: Authentication failed for 'https://github.com/x/y.git/'"},
		{"remote: Repository not found.", "fatal: repository 'https://github.com/x/y.git/' not found"},
		{"fatal: could not read Username for 'https://github.com': terminal prompts disabled"},
	}
	for _, ls := range authLines {
		if got := Classify(ls, Signal{}); got.Kind == KindCloneRefMissing {
			t.Errorf("auth/repo-missing line misclassified as ref-missing: %v", ls)
		}
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
			"Missing env var DATABASE_URL",        // older
			"panic: runtime error: nil ptr deref", // newer
		},
		Signal{},
	)
	if got.Kind != KindCrashLoop {
		t.Errorf("Kind = %q, want CrashLoop (most recent line)", got.Kind)
	}
}

func TestClassify_SpecificBuildDetectorBeatsGenericLaterLine(t *testing.T) {
	// A build log prints the SPECIFIC root cause (lockfile drift) early
	// and a GENERIC "build failed" / "command failed with exit code"
	// line at the very end. A naive reverse LINE walk would let the
	// generic tail win because it's later in the buffer. The build-
	// specific detector must win regardless of line position.
	got := Classify(
		[]string{
			"using pnpm@9.15.0",
			"ERR_PNPM_OUTDATED_LOCKFILE  Cannot install with \"frozen-lockfile\"", // specific, early
			"#14 ERROR: process \"/bin/sh -c pnpm install\" did not complete",
			"command failed with exit code 1", // generic, LATE
		},
		Signal{},
	)
	if got.Kind != KindLockfileDrift {
		t.Errorf("Kind = %q, want %q (specific build detector must beat generic later line)",
			got.Kind, KindLockfileDrift)
	}
	if got.Remediation == nil {
		t.Error("expected the lockfile-drift Remediation, got nil")
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
