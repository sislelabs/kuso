// Package failures classifies build and pod terminal failures into a
// fixed set of kinds so the UI can deep-link the right overlay tab
// with a human summary instead of dumping the user on the canvas
// with a red ring and 300 lines of raw stderr to grep through.
//
// This is the "what just went wrong" path that runs once per failure
// event. It's intentionally NOT the streaming log scanner — that
// belongs to internal/errorscan, which pre-aggregates ERROR-level log
// lines for the alert engine and dashboard. failures.Classify reads
// the last N lines of the dead container/build pod's logs (plus the
// pod's terminated container status when available) and returns one
// Classification with the kind, the tab the UI should open on, a
// one-line human summary, and the offending log line for highlight.
//
// Kinds are an enum string, not an iota, because they cross the wire
// into the bell-popover JSON + the persisted NotificationEvent row,
// and a stable string is forgiving when consumers ship at different
// versions. Add new kinds at the bottom of the const block; existing
// ones do not change spelling.
package failures

import (
	"regexp"
	"strings"
)

// Kind enumerates the failure classes the UI knows how to route. The
// zero value KindGeneric is the fallback when no detector matches —
// the UI still opens the Logs tab; it just shows a generic banner.
type Kind string

const (
	KindGeneric            Kind = "generic"
	KindMissingEnv         Kind = "missing_env"
	KindOOM                Kind = "oom"
	KindCrashLoop          Kind = "crash_loop"
	KindImagePullFailed    Kind = "image_pull_failed"
	KindPortConflict       Kind = "port_conflict"
	KindHealthcheckFailed  Kind = "healthcheck_failed"
	KindBuildCommandFailed Kind = "build_command_failed"
	// Build-time (pre-image) failure kinds. These come from the Docker /
	// buildkit / nixpacks / buildpacks build log, NOT the running pod.
	// Most carry an actionable Remediation with a copy-pasteable fix
	// because the fix lives in the user's repo (Dockerfile / lockfile),
	// not in kuso settings.
	KindDockerfileMissingCopy Kind = "dockerfile_missing_copy" // a COPY'd path / referenced file isn't in the build context
	KindLockfileDrift         Kind = "lockfile_drift"          // --frozen-lockfile / npm ci out of sync with package.json
	KindMissingBuildArg       Kind = "missing_build_arg"       // a build needs an ARG/env the build didn't pass
	KindDependencyResolution  Kind = "dependency_resolution"   // npm/pnpm/yarn/pip/go could not resolve a dependency or version
	KindDockerfileNotFound    Kind = "dockerfile_not_found"    // the configured Dockerfile path doesn't exist in the repo/subdir
	KindBuildOOM              Kind = "build_oom"               // the build pod (not the app) ran out of memory
	KindRegistryAuth          Kind = "registry_auth"           // pull/push to a registry denied (base image or cache)
	// KindCloneRefMissing is the clone init container failing because the
	// ref it was told to build no longer exists on the remote — the branch
	// was deleted or force-pushed while the build sat queued. This is NOT a
	// real failure (nothing is broken; the commit is simply unreachable), so
	// the poller diverts builds classified this way to CANCELLED rather than
	// FAILED and suppresses the @here page. Detected from the clone git
	// fatal; distinct from KindRegistryAuth (a genuine credential problem).
	KindCloneRefMissing Kind = "clone_ref_missing"
)

// Tab is the overlay tab slug the UI should open on. Kept as a string
// for the same wire-stability reason Kind is. The web's ServiceOverlay
// already accepts ?tab=<slug>; we just need to populate the right one.
type Tab string

const (
	TabLogs      Tab = "logs"
	TabVariables Tab = "variables"
	TabSettings  Tab = "settings"
	// TabBuild opens the Build settings section — the right place for
	// build-time failures whose fix is a build config (Dockerfile path,
	// build args, build memory) rather than runtime env.
	TabBuild Tab = "build"
)

// Remediation is the actionable "here's how to fix it" payload attached
// to classifications where kuso can name a concrete fix. It's optional
// (nil for kinds where we only know "something failed"). The UI renders
// Title as a heading, Detail as prose, and — when Fix is set — a
// copy-pasteable code block (a Dockerfile snippet, a CLI command, …).
// DocsAnchor, when set, is a slug into the kuso docs the UI can deep-link.
type Remediation struct {
	// Title is a short imperative headline, e.g. "Copy the patches
	// directory into the build context".
	Title string `json:"title"`
	// Detail is one or two sentences explaining the cause + fix.
	Detail string `json:"detail"`
	// Fix is an optional copy-pasteable snippet (Dockerfile lines, a
	// shell command). Empty when the fix isn't a single paste.
	Fix string `json:"fix,omitempty"`
	// FixLang hints the UI's syntax highlighter ("dockerfile", "bash",
	// "json"). Empty = plain.
	FixLang string `json:"fixLang,omitempty"`
	// DocsAnchor is an optional docs slug, e.g. "build/dockerfile".
	DocsAnchor string `json:"docsAnchor,omitempty"`
}

// Classification is the wire shape that travels with a failure event.
// All fields are populated by Classify; consumers can rely on Kind,
// Tab, and Summary being non-empty (Classify falls back to KindGeneric
// + a generic summary if no detector matches). LineHint is the raw
// log line that triggered the match — empty for kinds detected from
// pod status alone (OOM, ImagePullBackOff). LineNum is best-effort:
// the 1-based offset of LineHint inside the supplied logs slice;
// zero (omitted) when the kind didn't come from a log match.
type Classification struct {
	Kind     Kind   `json:"kind"`
	Tab      Tab    `json:"tab"`
	Summary  string `json:"summary"`
	LineHint string `json:"lineHint,omitempty"`
	LineNum  int    `json:"lineNum,omitempty"`
	// Remediation is the actionable fix when kuso recognises one. Nil
	// for generic / unrecognised failures (the UI then shows only the
	// summary + logs). Populated mostly by the build-time detectors.
	Remediation *Remediation `json:"remediation,omitempty"`
}

// Signal is the optional pod-status side-channel the caller can pass
// when a pod-watcher already has the terminated container's reason
// (CrashLoopBackOff, OOMKilled, ErrImagePull, ImagePullBackOff,
// CreateContainerConfigError). Build failures don't have these and
// pass an empty Signal. Both fields are strings to keep this package
// stdlib-only — no k8s.io imports — so the classifier can be unit-
// tested without faking a kube client.
type Signal struct {
	// Reason is the kubelet "waiting" or "terminated" reason. Examples:
	// "CrashLoopBackOff", "OOMKilled", "ErrImagePull",
	// "ImagePullBackOff", "CreateContainerConfigError".
	Reason string
	// ExitCode is the terminated container's exit code. 137 = SIGKILL
	// (often OOM); 139 = SIGSEGV; 0 = clean (won't be a failure event
	// in the first place). Zero value means the caller didn't have it
	// — Reason takes precedence.
	ExitCode int32
}

// Classify inspects pod-status signal + the last N log lines and
// returns a Classification. logLines should already be tail-sliced
// (the caller knows what "the relevant tail" looks like for its
// kind of failure — 5 lines for a Discord card, 50 for diagnostics).
//
// Detection order matters: more specific signals win over log regex
// matches. A pod with OOMKilled reason classifies as OOM even if its
// logs also contain "Address already in use" from an earlier boot.
func Classify(logLines []string, sig Signal) Classification {
	// Pod-status signals first — these are unambiguous when present.
	if c, ok := classifyFromSignal(sig); ok {
		return c
	}
	// Log-line regex matches second, in two phases.
	//
	// Phase 1 — build-specific detectors, PRIORITY-first and position-
	// independent. A build log routinely contains a generic "build
	// failed" / "command failed with exit code" line at the very END
	// as well as the specific root cause (missing COPY, lockfile drift,
	// …) printed EARLIER. A naive reverse LINE walk would let the
	// generic tail line win because it's later in the buffer. So we try
	// each build-specific detector across ALL lines first: the most
	// actionable cause wins regardless of where it appears.
	if c, ok := matchDetectors(logLines, true); ok {
		return c
	}
	// Phase 2 — runtime detectors, reverse LINE walk (most-recent line
	// wins). For the runtime kinds there's no "specific-vs-generic tail"
	// problem; the freshest line is genuinely the one that took the pod
	// down, so we preserve most-recent-wins here.
	for i := len(logLines) - 1; i >= 0; i-- {
		line := logLines[i]
		for _, d := range logDetectors {
			if d.buildTime {
				continue // handled in phase 1
			}
			if d.re.MatchString(line) {
				return buildClassification(d, line, i, logLines)
			}
		}
	}
	return Classification{
		Kind:    KindGeneric,
		Tab:     TabLogs,
		Summary: "Deploy failed. See logs for details.",
	}
}

// matchDetectors tries every detector whose buildTime flag equals
// `buildTime`, in their declared (specific-first) order, against ALL
// log lines — so detector priority dominates line position. For the
// winning detector it picks the MOST-RECENT matching line (reverse
// scan) so a repeated failure surfaces its freshest occurrence.
// Returns ok=false when no matching detector fired.
func matchDetectors(logLines []string, buildTime bool) (Classification, bool) {
	for _, d := range logDetectors {
		if d.buildTime != buildTime {
			continue
		}
		for i := len(logLines) - 1; i >= 0; i-- {
			if d.re.MatchString(logLines[i]) {
				return buildClassification(d, logLines[i], i, logLines), true
			}
		}
	}
	return Classification{}, false
}

// buildClassification assembles the Classification for a detector match
// on `line` at 0-based index `i`, running the detector's remediation
// builder (which gets the whole tail for cross-line context).
func buildClassification(d logDetector, line string, i int, tail []string) Classification {
	c := Classification{
		Kind:     d.kind,
		Tab:      d.tab,
		Summary:  d.summarize(line),
		LineHint: truncateLine(line),
		LineNum:  i + 1,
	}
	if d.remediate != nil {
		c.Remediation = d.remediate(line, tail)
	}
	return c
}

// classifyFromSignal maps kubelet container-state reasons to Kinds.
// Returns ok=false when the signal doesn't match any known reason;
// the caller then falls through to log-regex matching.
func classifyFromSignal(sig Signal) (Classification, bool) {
	switch sig.Reason {
	case "OOMKilled":
		return Classification{
			Kind:    KindOOM,
			Tab:     TabLogs,
			Summary: "Pod ran out of memory. Bump the memory request in Settings → Scale.",
		}, true
	case "CrashLoopBackOff":
		return Classification{
			Kind:    KindCrashLoop,
			Tab:     TabLogs,
			Summary: "Pod keeps crashing. See logs for the failing stack.",
		}, true
	case "ErrImagePull", "ImagePullBackOff":
		return Classification{
			Kind:    KindImagePullFailed,
			Tab:     TabSettings,
			Summary: "Couldn't pull the image. Check the registry credentials in Settings → Source.",
		}, true
	case "CreateContainerConfigError":
		// CreateContainerConfigError on a kuso pod almost always means
		// a referenced Secret/ConfigMap key is missing — i.e. an env
		// var the spec asked for isn't there. Route to Variables.
		return Classification{
			Kind:    KindMissingEnv,
			Tab:     TabVariables,
			Summary: "Container config error — a referenced env-var secret is missing.",
		}, true
	}
	// Exit-code-only fallback when Reason was blank (some kubelet
	// versions leave it empty on the terminated state). 137 means
	// SIGKILL which on Kubernetes is almost always the OOM-killer.
	if sig.Reason == "" && sig.ExitCode == 137 {
		return Classification{
			Kind:    KindOOM,
			Tab:     TabLogs,
			Summary: "Pod ran out of memory (SIGKILL). Bump memory in Settings → Scale.",
		}, true
	}
	return Classification{}, false
}

// logDetector pairs a regex with the Kind it implies plus a short
// per-kind summarizer that pulls a human one-liner out of the matched
// line. Detectors are tried in order; the first match wins.
type logDetector struct {
	kind Kind
	tab  Tab
	re   *regexp.Regexp
	// buildTime marks the pre-image build-log detectors (missing COPY,
	// lockfile drift, build OOM, …). Classify tries these PRIORITY-first
	// and position-independent (across all lines) so a generic "build
	// failed" tail line can't outrank the specific root cause printed
	// earlier. Runtime detectors leave this false and use the
	// most-recent-line reverse walk instead.
	buildTime bool
	summarize func(line string) string
	// remediate is an optional builder for the actionable fix. It
	// receives the matched line AND the full tail (for cross-line
	// context). Returns nil when no concrete fix can be named for this
	// particular match. Nil remediate = no remediation (runtime kinds).
	remediate func(line string, tail []string) *Remediation
}

// logDetectors is the regex-driven detection list. Order is meaningful
// — more specific patterns come first. Keep this short: every line of
// every failed pod gets walked over it, so adding a slow regex here
// is a noticeable cost.
var logDetectors = []logDetector{
	// ── Build-time detectors (Docker / buildkit / package managers) ──
	// These come FIRST: a build log can contain a generic "build failed"
	// line as well as the specific root cause, and we want the specific,
	// actionable one to win. Each carries a Remediation because the fix
	// lives in the user's repo, not kuso settings.

	// Clone target ref is gone. The clone init container was told to fetch
	// a specific branch/SHA that no longer exists on the remote — the
	// branch was deleted (squash-merge + delete) or force-pushed while the
	// build sat queued behind the concurrency limit. git prints one of a
	// handful of distinctive fatals. This MUST win over the generic
	// exit-128 / "build failed" lines so the poller can divert the build to
	// CANCELLED instead of paging @here. Kept ahead of KindRegistryAuth so
	// a "couldn't find remote ref" never reads as a credentials problem.
	{
		// Regex is deliberately NARROW: only phrases git emits when the
		// FETCH of the target ref fails. Broader git plumbing errors
		// ("did not match any file(s) known to git", "invalid/ambiguous
		// reference/argument", "reference broken") were removed — a user's
		// own build step (RUN git checkout / git describe / a corrupted
		// checkout) prints those, and matching them would silently CANCEL a
		// genuine build failure and suppress its page. These alternatives
		// are specific to fetching the remote ref the build was told to
		// build; a normal RUN step does not produce them.
		kind:      KindCloneRefMissing,
		tab:       TabLogs,
		buildTime: true,
		re:        regexp.MustCompile(`(?i)couldn't find remote ref|remote branch \S+ not found|fatal: reference is not a tree:|unadvertised object|wanted ref \S+ not found|fatal: couldn't find remote ref`),
		summarize: func(line string) string {
			return "The branch or commit this build targeted no longer exists — it was deleted or force-pushed while the build was queued. No action needed."
		},
		remediate: func(line string, tail []string) *Remediation {
			return &Remediation{
				Title:  "Nothing to fix — the ref was deleted",
				Detail: "The branch was deleted or force-pushed (e.g. a squash-merge that removed the PR branch) while this build sat in the queue, so the clone step couldn't find the commit. This is expected and safe to ignore; the build is marked cancelled, not failed.",
			}
		},
	},

	// pnpm/npm patch file missing from the build context. The package
	// manager reads `patchedDependencies` from package.json, tries to
	// open the patch file, and ENOENTs because the Dockerfile copied
	// package.json + lockfile but NOT the patches/ dir. Very common with
	// Payload/pnpm setups. The fix is a one-line COPY before install.
	{
		kind:      KindDockerfileMissingCopy,
		tab:       TabBuild,
		buildTime: true,
		re:        regexp.MustCompile(`(?i)ENOENT.{0,80}\.patch|no such file or directory.{0,40}patches/|failed to (?:read|open).{0,40}\.patch`),
		summarize: func(line string) string {
			if f := extractPatchPath(line); f != "" {
				return "Build can't find patch file " + f + " — it's declared in package.json but not COPYed into the image."
			}
			return "Build can't find a pnpm/npm patch file — the patches/ dir isn't COPYed into the build context."
		},
		remediate: func(line string, tail []string) *Remediation {
			return &Remediation{
				Title:      "Copy the patches directory into the build context",
				Detail:     "Your package.json declares patchedDependencies, but the Dockerfile copies package.json and the lockfile without the patches/ directory — so the package manager can't find the patch at install time. Add a COPY for patches/ before the install step (in every stage that runs install).",
				Fix:        "COPY package.json pnpm-lock.yaml* ./\nCOPY patches/ ./patches/\nRUN pnpm install --frozen-lockfile",
				FixLang:    "dockerfile",
				DocsAnchor: "build/dockerfile-context",
			}
		},
	},
	// Lockfile out of sync with package.json under a frozen/CI install.
	// pnpm: "ERR_PNPM_OUTDATED_LOCKFILE"; npm ci: "can only install
	// packages when your package.json and package-lock.json are in sync";
	// yarn: "lockfile needs to be updated".
	{
		kind:      KindLockfileDrift,
		tab:       TabBuild,
		buildTime: true,
		re:        regexp.MustCompile(`(?i)ERR_PNPM_OUTDATED_LOCKFILE|lockfile.{0,40}(?:out of date|outdated|needs to be updated|not up to date)|npm ci.{0,80}out of sync|can only install packages when your package\.json and package-lock\.json.{0,20}in sync|frozen-lockfile`),
		summarize: func(line string) string {
			return "Lockfile is out of sync with package.json — a frozen/CI install can't reconcile it."
		},
		remediate: func(line string, tail []string) *Remediation {
			pm := detectPackageManager(tail)
			fix := "npm install   # regenerate package-lock.json, then commit it"
			switch pm {
			case "pnpm":
				fix = "pnpm install   # regenerate pnpm-lock.yaml, then commit it"
			case "yarn":
				fix = "yarn install   # regenerate yarn.lock, then commit it"
			}
			return &Remediation{
				Title:      "Update and commit your lockfile",
				Detail:     "The build runs a frozen/CI install (no lockfile writes), but your lockfile doesn't match package.json. Run a normal install locally to regenerate the lockfile, commit it, and push.",
				Fix:        fix,
				FixLang:    "bash",
				DocsAnchor: "build/lockfile",
			}
		},
	},
	// Configured Dockerfile path doesn't exist. buildkit: "failed to
	// read dockerfile" / "Dockerfile: no such file". Usually a wrong
	// build path / subdir in service settings.
	{
		kind:      KindDockerfileNotFound,
		tab:       TabBuild,
		buildTime: true,
		re:        regexp.MustCompile(`(?i)failed to read dockerfile|dockerfile.{0,20}no such file|cannot locate specified Dockerfile|unable to prepare context.{0,40}dockerfile`),
		summarize: func(line string) string {
			return "The configured Dockerfile wasn't found in the build path."
		},
		remediate: func(line string, tail []string) *Remediation {
			return &Remediation{
				Title:      "Fix the build path or Dockerfile location",
				Detail:     "kuso looked for a Dockerfile in the configured build path and didn't find one. Check Settings → Build for the path (monorepo subdir) and the Dockerfile name, and confirm the file is committed at that location.",
				DocsAnchor: "build/dockerfile",
			}
		},
	},
	// Build ran out of memory (the BUILD pod, not the app). buildkit /
	// node often print "JavaScript heap out of memory" or the step is
	// killed (exit 137) during a heavy `next build` / webpack.
	{
		kind:      KindBuildOOM,
		tab:       TabBuild,
		buildTime: true,
		re:        regexp.MustCompile(`(?i)JavaScript heap out of memory|FATAL ERROR:.{0,40}heap|Killed\s*$|signal: killed|out of memory.{0,20}(?:build|compil)`),
		summarize: func(line string) string {
			return "The build ran out of memory. Raise the build memory limit in Settings → Build."
		},
		remediate: func(line string, tail []string) *Remediation {
			return &Remediation{
				Title:      "Increase the build memory limit",
				Detail:     "The build step was killed for exceeding its memory budget (common for large Next.js / webpack builds). Raise the build memory in Settings → Build. For Node builds you can also cap the heap with NODE_OPTIONS.",
				Fix:        "ENV NODE_OPTIONS=--max-old-space-size=4096",
				FixLang:    "dockerfile",
				DocsAnchor: "build/resources",
			}
		},
	},
	// Registry auth / pull denied for the base image or build cache.
	{
		kind:      KindRegistryAuth,
		tab:       TabBuild,
		buildTime: true,
		re:        regexp.MustCompile(`(?i)(?:pull access denied|denied: requested access to the resource is denied|unauthorized: authentication required|error response from daemon.{0,40}unauthorized|failed to authorize)`),
		summarize: func(line string) string {
			return "A registry denied the build (base image or cache pull) — check the image name / credentials."
		},
		remediate: func(line string, tail []string) *Remediation {
			return &Remediation{
				Title:      "Check the base image and registry credentials",
				Detail:     "The build couldn't pull from a registry — usually a typo'd base image, a private base image without credentials, or a rate-limited Docker Hub pull. Verify the FROM line and, for private bases, add registry credentials.",
				DocsAnchor: "build/registry",
			}
		},
	},
	// Generic dependency resolution failures across ecosystems. Kept
	// after the specific lockfile/patch detectors so those win.
	{
		kind:      KindDependencyResolution,
		tab:       TabBuild,
		buildTime: true,
		re:        regexp.MustCompile(`(?i)ERR_PNPM_NO_MATCHING_VERSION|npm ERR!.{0,40}(?:404|notarget|ETARGET)|Could not resolve dependency|No matching version found|Cannot find module|pip.{0,40}Could not find a version|go: .*: unknown revision|ERROR: No matching distribution found`),
		summarize: func(line string) string {
			return "A dependency couldn't be resolved. " + briefLine(line)
		},
		remediate: func(line string, tail []string) *Remediation {
			return &Remediation{
				Title:      "Fix the unresolved dependency",
				Detail:     "The package manager couldn't resolve a dependency or version. Check for a typo'd package name, a version that doesn't exist, or a private package that needs registry auth, then update and commit your manifest + lockfile.",
				DocsAnchor: "build/dependencies",
			}
		},
	},

	// ── Runtime detectors (existing) ─────────────────────────────────
	// Missing env var — handled before the generic "Error:" catchers
	// because a missing-env error usually says ERROR somewhere too.
	{
		kind: KindMissingEnv,
		tab:  TabVariables,
		re:   regexp.MustCompile(`(?i)(missing|undefined|not set|required).{0,40}env|KeyError:.{0,40}env|environment variable .* (?:not set|required|missing)`),
		summarize: func(line string) string {
			if name := extractEnvName(line); name != "" {
				return "Missing env var: " + name
			}
			return "Missing env var — see logs for the variable name."
		},
	},
	// Port-conflict — happens when the container tries to listen on a
	// port already bound (commonly because the user changed the port
	// in settings but the prior process didn't release it).
	{
		kind: KindPortConflict,
		tab:  TabLogs,
		re:   regexp.MustCompile(`(?i)address already in use|EADDRINUSE|bind: address already in use`),
		summarize: func(line string) string {
			if p := extractPort(line); p != "" {
				return "Port " + p + " is already in use inside the container."
			}
			return "Container port is already in use."
		},
	},
	// Healthcheck — readiness/liveness failures echoed by some
	// frameworks ("Health check failed", "probe failed").
	{
		kind: KindHealthcheckFailed,
		tab:  TabLogs,
		re:   regexp.MustCompile(`(?i)(readiness|liveness) probe failed|health(?: ?check)? (?:failed|did not pass)`),
		summarize: func(line string) string {
			return "Health probe failing — pod isn't accepting traffic."
		},
	},
	// Build command failed — kaniko / nixpacks / buildpacks usually
	// echo "command failed with exit code N" or "Error: build failed".
	{
		kind: KindBuildCommandFailed,
		tab:  TabLogs,
		re:   regexp.MustCompile(`(?i)build failed|command failed with exit code|error building image|nixpacks build failed|buildpack failed`),
		summarize: func(line string) string {
			return "Build command exited non-zero. " + briefLine(line)
		},
	},
	// Crash-loop catch-all — Go/Python/Node panics. Last in the list
	// so more specific causes (missing env, port conflict) classify
	// first when both patterns match.
	{
		kind: KindCrashLoop,
		tab:  TabLogs,
		re:   regexp.MustCompile(`^panic:|Traceback \(most recent call last\)|UnhandledPromiseRejection|java\.lang\..*Exception`),
		summarize: func(line string) string {
			return "Process crashed: " + briefLine(line)
		},
	},
}

// extractEnvName pulls the env-var name out of common missing-env log
// shapes:
//
//	"Missing env var DATABASE_URL"
//	"KeyError: 'DATABASE_URL'"
//	"environment variable DATABASE_URL not set"
//
// Returns "" when no name is extractable. We accept ASCII identifier
// shapes (uppercase letters, digits, underscore) — env var names that
// don't match that convention exist but are rare and the cost of a
// false miss is just a less-specific summary string.
var envNameRE = regexp.MustCompile(`[A-Z][A-Z0-9_]{1,63}`)

func extractEnvName(line string) string {
	return envNameRE.FindString(line)
}

// extractPatchPath pulls the offending patch-file path out of an ENOENT
// line like:
//
//	ENOENT: no such file or directory, open '/app/patches/@payloadcms__ui@3.85.1.patch'
//
// Returns "" when no .patch path is present. We surface the basename
// (patches/<file>.patch) since the absolute /app prefix is a build-stage
// detail the user doesn't care about.
var patchPathRE = regexp.MustCompile(`(patches/[^\s'"]+\.patch)`)

func extractPatchPath(line string) string {
	if m := patchPathRE.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	// Fall back to any *.patch token so we still name the file even when
	// it isn't under a patches/ dir.
	if m := regexp.MustCompile(`([^\s'"/]+\.patch)`).FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// detectPackageManager sniffs the build tail for which JS package
// manager is in play so lockfile-drift remediation names the right
// command + lockfile. Returns "pnpm" | "yarn" | "npm" (default npm).
func detectPackageManager(tail []string) string {
	for _, l := range tail {
		switch {
		case strings.Contains(l, "pnpm") || strings.Contains(l, "pnpm-lock"):
			return "pnpm"
		case strings.Contains(l, "yarn") || strings.Contains(l, "yarn.lock"):
			return "yarn"
		}
	}
	return "npm"
}

// extractPort pulls a TCP port number out of a port-conflict log line.
// Looks for "port N" or ":N" patterns. Returns "" when not found —
// the summary then omits the specific port and just says "the port".
var portRE = regexp.MustCompile(`port\s+(\d{2,5})|:(\d{2,5})\b`)

func extractPort(line string) string {
	m := portRE.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	for i := 1; i < len(m); i++ {
		if m[i] != "" {
			return m[i]
		}
	}
	return ""
}

// truncateLine bounds the LineHint that travels in the notification
// payload. Log lines can be megabytes long (some structured loggers
// dump entire request bodies); we never want to push that through a
// webhook or store it on a NotificationEvent row.
const maxLineHint = 400

func truncateLine(s string) string {
	s = strings.TrimRight(s, "\r\n")
	if len(s) <= maxLineHint {
		return s
	}
	return s[:maxLineHint] + "…"
}

// briefLine is truncateLine but tighter, for embedding inside a
// summary string. The full line still travels in LineHint for the
// UI to render in a code block.
const maxBriefLine = 120

func briefLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxBriefLine {
		return s
	}
	return s[:maxBriefLine] + "…"
}
