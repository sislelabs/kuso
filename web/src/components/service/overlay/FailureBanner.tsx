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
  | "build_command_failed";

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
    body: "The last failure line is highlighted in the log viewer below.",
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
    body: "See the highlighted line in the build log below for the failing step.",
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
  // onDismiss clears the banner. Wired to the overlay so closing the
  // service overlay implicitly dismisses; clicking the X dismisses
  // explicitly without closing.
  onDismiss?: () => void;
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
export function FailureBanner({ kind, lineHint, onDismiss }: Props) {
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
