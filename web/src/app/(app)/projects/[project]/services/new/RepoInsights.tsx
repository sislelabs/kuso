"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { CheckCircle2, CircleAlert, ExternalLink, Info, Loader2, RotateCw, ShieldCheck } from "lucide-react";
import { Button, buttonVariants } from "@/components/ui/button";
import { Callout } from "@/components/ui/callout";
import { api } from "@/lib/api-client";
import { friendlyApiError } from "@/features/projects/names";
import { DETECTION_RULES, FALLBACK_RUNTIME, type RepoDetection } from "./useRepoDetection";
import type { PickedRepo } from "./RepoPicker";

function where(path: string): string {
  const p = path.trim().replace(/^\/+|\/+$/g, "");
  return p ? `${p}/` : "the repository root";
}

// RuntimeExplainer says what detection found, or — when it found
// nothing or couldn't look — what it looked for and where, and why the
// preselected runtime is a reasonable default.
export function RuntimeExplainer({
  detection,
  path,
  runtime,
}: {
  detection: RepoDetection;
  path: string;
  runtime: string;
}) {
  const { state, result } = detection;
  if (state === "idle") return null;
  if (state === "detecting") {
    return (
      <p className="flex items-center gap-1.5 text-xs text-[var(--text-secondary)]" role="status">
        <Loader2 className="h-3 w-3 motion-safe:animate-spin" />
        Looking at {where(path)}…
      </p>
    );
  }
  const overridden = runtime !== "" && runtime !== (result?.runtime ?? FALLBACK_RUNTIME);
  if (state === "done" && result && result.reason !== "fallback") {
    return (
      <p className="flex items-start gap-1.5 text-xs text-[var(--text-secondary)]">
        <CheckCircle2 className="mt-0.5 h-3 w-3 shrink-0 text-[var(--success)]" />
        <span>
          {result.reason.replace(/ detected$/, "")} found in {where(path)}, so kuso picked{" "}
          <span className="font-mono">{result.runtime}</span>
          {result.port ? (
            <>
              {" "}
              on port <span className="font-mono">{result.port}</span>
            </>
          ) : null}
          .{overridden && " You've overridden it."}
        </span>
      </p>
    );
  }
  // Nothing matched, or the repo couldn't be read.
  return (
    <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-2.5 text-xs leading-relaxed text-[var(--text-secondary)]">
      <p className="flex items-start gap-1.5 text-[var(--text-primary)]">
        <Info className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--warning)]" />
        {/* The failure's reason is in RepoReadFailureCallout; don't repeat it. */}
        {state === "failed" ? (
          <span>Runtime not detected.</span>
        ) : (
          <span>No known build file in {where(path)}.</span>
        )}
      </p>
      <p className="mt-1.5">It looks in {where(path)} for, in order:</p>
      <ul className="mt-1 space-y-0.5 pl-5">
        {DETECTION_RULES.map((r) => (
          <li key={r.runtime} className="list-disc">
            {r.files} → <span className="font-mono">{r.runtime}</span>
          </li>
        ))}
      </ul>
      <p className="mt-1.5">
        {runtime === FALLBACK_RUNTIME ? "Using" : "Default is"} <span className="font-mono">{FALLBACK_RUNTIME}</span>:
        it inspects the code at build time and also handles Ruby, PHP, Java, Elixir and more.
        {!path.trim() && " If the app lives in a subfolder, set its root directory."}
      </p>
    </div>
  );
}

// RepoReadFailureCallout is the one place the page says why the repo
// couldn't be read, with the fix (grant access / token / setup), a retry,
// and a way past detection so the user is never stuck on it.
export function RepoReadFailureCallout({
  detection,
  installURL,
  isAdmin,
  onContinue,
  className,
}: {
  detection: RepoDetection;
  installURL: string;
  isAdmin: boolean;
  // Opens the manual runtime picker. Omit when it's already on screen.
  onContinue?: () => void;
  className?: string;
}) {
  const f = detection.state === "failed" ? detection.failure : null;
  if (!f) return null;
  const linkClass = buttonVariants({ variant: "secondary", size: "sm" });
  let fix: React.ReactNode = null;
  if (f.action === "grant-access" && installURL) {
    fix = (
      <a href={installURL} target="_blank" rel="noreferrer" className={linkClass}>
        Grant access on GitHub
        <ExternalLink />
      </a>
    );
  } else if (f.action === "setup-github" && isAdmin) {
    fix = (
      <Link href="/settings/github" className={linkClass}>
        Set up GitHub
      </Link>
    );
  }
  return (
    <Callout tone="warning" role="alert" title={f.title} className={className}>
      <p className="text-xs leading-relaxed">
        {f.detail}
        {f.action === "use-token" && " To add a token, pick the repo again from the Access token tab."}
        {f.status ? <span className="ml-1 font-mono text-[var(--text-tertiary)]">HTTP {f.status}</span> : null}
      </p>
      <div className="mt-2.5 flex flex-wrap items-center gap-2">
        {fix}
        {f.retryable && (
          <Button type="button" size="sm" variant="secondary" onClick={detection.retry}>
            <RotateCw />
            Retry
          </Button>
        )}
        {onContinue && (
          <Button type="button" size="sm" variant="ghost" onClick={onContinue}>
            Continue anyway — choose runtime manually
          </Button>
        )}
      </div>
    </Callout>
  );
}

type AccessState = { key: string; state: "checking" | "ok" | "fail"; message?: string };

// AccessCheck asks the server whether kuso's GitHub App installation can
// read (and so clone) the repo. Runs once automatically per pick — it is
// one GitHub API call — and can be re-run after changing the App's
// repository access on github.com.
export function AccessCheck({
  picked,
  appConfigured,
  detectionFailed = false,
}: {
  picked: PickedRepo;
  appConfigured: boolean;
  // RepoReadFailureCallout already explains an unreadable repo.
  detectionFailed?: boolean;
}) {
  const key = `${picked.installationId}/${picked.repo.fullName}`;
  const [access, setAccess] = useState<AccessState | null>(null);
  const pasted = !!picked.byUrl;
  const isGithub = !pasted || /^https?:\/\/(www\.)?github\.com\//i.test(picked.byUrl?.url ?? "");
  // A pasted repo with a token clones with that token; the App is moot.
  const relevant = appConfigured && isGithub && !picked.byUrl?.token;

  const run = async () => {
    const [owner, repo] = picked.repo.fullName.split("/");
    setAccess({ key, state: "checking" });
    try {
      const res = await api<{ ok: boolean; error?: string }>("/api/github/check-repo", {
        method: "POST",
        body: { owner, repo, installationId: picked.installationId },
      });
      setAccess({ key, state: res.ok ? "ok" : "fail", message: res.error });
    } catch (e) {
      setAccess({ key, state: "fail", message: friendlyApiError(e, "Check failed") });
    }
  };

  useEffect(() => {
    if (relevant) void run();
    // One automatic run per pick.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, relevant]);

  if (!relevant) return null;
  const cur = access?.key === key ? access : null;
  if (detectionFailed && !pasted && cur?.state === "fail") return null;

  let verdict: React.ReactNode = null;
  if (cur?.state === "checking") {
    verdict = (
      <span className="flex items-center gap-1.5 text-[var(--text-secondary)]">
        <Loader2 className="h-3 w-3 motion-safe:animate-spin" />
        Checking…
      </span>
    );
  } else if (cur?.state === "ok") {
    verdict = (
      <span className="flex items-center gap-1.5 text-[var(--success)]">
        <CheckCircle2 className="h-3.5 w-3.5" />
        kuso&apos;s GitHub App can clone this repo{pasted ? "" : " and will build on every push"}.
      </span>
    );
  } else if (cur?.state === "fail") {
    verdict = pasted ? (
      <span className="flex items-start gap-1.5 text-[var(--text-secondary)]">
        <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <span>
          The GitHub App isn&apos;t installed for this repo. kuso can still clone it because it&apos;s public,
          but pushes won&apos;t start builds; turn on the deploy hook in the service settings for that.
        </span>
      </span>
    ) : (
      <span className="flex items-start gap-1.5 text-[var(--error)]">
        <CircleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <span>{cur.message || "kuso's GitHub App can't read this repo."}</span>
      </span>
    );
  }

  return (
    <div className="flex flex-col gap-2 text-xs sm:flex-row sm:items-center">
      <Button
        type="button"
        size="sm"
        variant="outline"
        onClick={() => void run()}
        disabled={cur?.state === "checking"}
        title="Asks GitHub whether kuso's App installation can read this repository, which it needs to clone it for builds and to receive push events."
        className="self-start"
      >
        <ShieldCheck className="h-3.5 w-3.5" />
        Check access
      </Button>
      <div className="min-w-0" role="status">
        {verdict}
      </div>
    </div>
  );
}
