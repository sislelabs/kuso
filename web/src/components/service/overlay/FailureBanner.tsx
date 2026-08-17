"use client";

import { AlertTriangle, X } from "lucide-react";
import { useState } from "react";

// FailureKind mirrors the server-side internal/failures.Kind enum.
// Stable strings — adding a new kind here means adding the matching
// case in COPY below; unknown kinds fall through to the generic copy
// so a server that ships a new kind ahead of the web bundle still
// renders something coherent.
export type FailureKind =
  | "generic"
  | "missing_env"
  | "oom"
  | "crash_loop"
  | "image_pull_failed"
  | "port_conflict"
  | "healthcheck_failed"
  | "build_command_failed"
  // Build-time (pre-image) kinds. These come from the build log, not a
  // running pod, and most carry a server-computed Remediation with a
  // copy-pasteable fix — see the `remediation` prop.
  | "dockerfile_missing_copy"
  | "lockfile_drift"
  | "missing_build_arg"
  | "dependency_resolution"
  | "dockerfile_not_found"
  | "build_oom"
  | "registry_auth"
  | "clone_ref_missing";

interface CopyPair {
  // headline reads as the bold first line ("Build crashed: missing env
  // var."); body is the actionable hint underneath ("Add it below or
  // paste a ${{ addon.KEY }} reference.").
  headline: string;
  body: string;
}

const COPY: Record<FailureKind, CopyPair> = {
  missing_env: {
    headline: "Build crashed: missing env var.",
    body: "Add it below, or paste a ${{ addon.KEY }} reference to wire it from a managed addon.",
  },
  oom: {
    headline: "Pod ran out of memory.",
    body: "Bump the memory request in Settings → Scale, or check the logs for a leak.",
  },
  crash_loop: {
    headline: "Pod keeps crashing.",
    body: "Check the log viewer below — the crash is usually in the last few lines before each restart.",
  },
  image_pull_failed: {
    headline: "Couldn't pull the image.",
    body: "Check the registry credentials and image tag in Settings → Source.",
  },
  port_conflict: {
    headline: "Container port is already in use.",
    body: "Something inside the container is binding the same port the service is configured for.",
  },
  healthcheck_failed: {
    headline: "Health probe failing.",
    body: "The pod isn't accepting traffic on the configured port. Check the app's startup time and probe path.",
  },
  build_command_failed: {
    headline: "Build command exited non-zero.",
    body: "See the build log below — the failing step is the last command before the non-zero exit.",
  },
  dockerfile_missing_copy: {
    headline: "A COPY in the Dockerfile referenced a missing path.",
    body: "The file isn't in the build context — check .dockerignore and the build path in Settings → Source.",
  },
  lockfile_drift: {
    headline: "Lockfile is out of sync with the manifest.",
    body: "The install ran with a frozen lockfile. Re-run the install locally, commit the updated lockfile, and push.",
  },
  missing_build_arg: {
    headline: "Build needed an ARG that wasn't provided.",
    body: "Add it as a build arg in Settings → Build, or give it a default in the Dockerfile.",
  },
  dependency_resolution: {
    headline: "A dependency couldn't be resolved.",
    body: "The package or version doesn't exist, or the registry was unreachable. Check the failing package in the log below.",
  },
  dockerfile_not_found: {
    headline: "Dockerfile not found at the configured path.",
    body: "Check the Dockerfile path in Settings → Source — it's relative to the build path, not the repo root.",
  },
  build_oom: {
    headline: "The build ran out of memory.",
    body: "This is the BUILD pod, not your app. Raise the build memory limit in instance settings, or reduce build parallelism.",
  },
  registry_auth: {
    headline: "Registry denied the pull or push.",
    body: "Check the registry credentials in Settings → Build. A private base image needs auth too.",
  },
  clone_ref_missing: {
    headline: "The git ref no longer exists.",
    body: "The branch or commit was deleted or force-pushed away. Pick an existing branch and redeploy.",
  },
  generic: {
    headline: "Deploy failed.",
    body: "See logs below for details.",
  },
};

interface Props {
  // kind is the server-classified failure type. We accept `string`
  // (not the strict union) so a future server kind not yet known to
  // the web bundle still renders — falls back to "generic" copy.
  kind?: string;
  // lineHint is the single offending log line the classifier extracted.
  // Rendered in a code block under the body when provided. Truncation
  // happens server-side (max ~400 chars) so we don't need to clamp here.
  lineHint?: string;
  // remediation is the server-computed, actionable fix for this
  // failure (internal/failures attaches one to most BUILD-time kinds).
  // When present it renders below the copy as a titled block with a
  // copy-pasteable snippet and a docs link.
  //
  // This used to be dropped on the floor: the classifier computed a
  // Remediation, BuildRow rendered it, and this banner — the other
  // surface users hit — showed only "Deploy failed. See logs below."
  // The server's best diagnostic reached one of two UIs.
  remediation?: FailureRemediation;
  // onDismiss clears the banner. Wired to the overlay so closing the
  // service overlay implicitly dismisses; clicking the X dismisses
  // explicitly without closing.
  onDismiss?: () => void;
}

// FailureRemediation mirrors the server's failures.Remediation and the
// web API's BuildFailureRemediation. Declared structurally (not
// imported) so this presentational component stays free of a features/
// dependency.
export interface FailureRemediation {
  title: string;
  detail?: string;
  fix?: string;
  fixLang?: string;
  docsAnchor?: string;
}

// FailureBanner shows up at the top of the routed overlay tab when a
// bell-popover click deep-links into a classified failure. Visually
// it's a red-accented strip with a short headline + hint and (when the
// classifier had one) the offending log line in a code block.
//
// Why a banner instead of a toast: toasts vanish in 4s; a failure
// you've just clicked into deserves to stay visible until the user
// dismisses or navigates away. Inline placement also keeps the
// affordance scoped to the right tab — variables tab shows env-var
// hints, logs tab shows crash hints, etc.
export function FailureBanner({ kind, lineHint, remediation, onDismiss }: Props) {
  const [dismissed, setDismissed] = useState(false);
  if (dismissed) return null;
  const key = (kind && (COPY as Record<string, CopyPair>)[kind])
    ? (kind as FailureKind)
    : "generic";
  const copy = COPY[key];
  return (
    <div
      role="alert"
      className="mb-4 rounded-lg border border-[var(--error)]/40 bg-[var(--error)]/5 px-4 py-3 text-sm text-[var(--text-primary)]"
    >
      <div className="flex items-start gap-3">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[var(--error)]" />
        <div className="min-w-0 flex-1">
          <div className="font-medium">{copy.headline}</div>
          <div className="mt-0.5 text-[var(--text-secondary)]">{copy.body}</div>
          {lineHint ? (
            <pre className="mt-2 max-h-32 overflow-auto whitespace-pre-wrap rounded-md bg-[var(--bg-secondary)] px-2 py-1.5 font-mono text-[0.75rem] text-[var(--text-secondary)]">
              {lineHint}
            </pre>
          ) : null}
          {remediation ? (
            <div className="mt-2 rounded-md border border-[var(--error)]/30 bg-[var(--bg-primary)]/40 p-2">
              <div className="text-[0.8rem] font-semibold">{remediation.title}</div>
              {remediation.detail ? (
                <p className="mt-0.5 text-[0.8rem] leading-snug text-[var(--text-secondary)]">
                  {remediation.detail}
                </p>
              ) : null}
              {remediation.fix ? (
                <pre className="mt-1.5 max-h-40 overflow-auto whitespace-pre-wrap rounded bg-[var(--bg-secondary)] px-2 py-1.5 font-mono text-[0.75rem]">
                  {remediation.fix}
                </pre>
              ) : null}
              {remediation.docsAnchor ? (
                <a
                  href={remediation.docsAnchor}
                  target="_blank"
                  rel="noreferrer"
                  className="mt-1.5 inline-block font-mono text-[0.7rem] underline underline-offset-2 text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
                >
                  read the docs →
                </a>
              ) : null}
            </div>
          ) : null}
        </div>
        <button
          type="button"
          aria-label="Dismiss failure banner"
          onClick={() => {
            setDismissed(true);
            onDismiss?.();
          }}
          className="text-[var(--text-tertiary)] transition-colors hover:text-[var(--text-primary)]"
        >
          <X className="h-4 w-4" />
        </button>
      </div>
    </div>
  );
}
