"use client";

import { useEffect, useState } from "react";
import { CheckCircle2, ExternalLink, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { lastDeployedAt } from "@/features/projects/facts";
import type { KusoEnvironment } from "@/types/projects";
import { firstLiveSeen, markFirstLiveSeen } from "../overlay/FirstDeployProgress";

// FirstLiveNotice is the moment a service's first rollout completes: where
// it's live, with copy and open. It shows once per service (remembered in
// localStorage) on whichever tab is open, and can be dismissed. The create
// flow lands on the service page, so the first deploy is watched there.
export function FirstLiveNotice({
  project,
  service,
  env,
  url,
  live,
}: {
  project: string;
  service: string;
  env: KusoEnvironment | undefined;
  url: string | undefined;
  // The selector says the env is serving.
  live: boolean;
}) {
  // The moment is watching a service that has never been live become
  // live: seen not-yet-live on this mount, then live. A service that was
  // already live when opened never qualifies, whatever its timestamps say.
  const neverLive = !!env && !live && !lastDeployedAt(env);
  const [sawNeverLive, setSawNeverLive] = useState(false);
  if (neverLive && !sawNeverLive) setSawNeverLive(true);
  const [state, setState] = useState<"unknown" | "show" | "hidden">("unknown");
  const eligible = !!url && live && sawNeverLive;

  useEffect(() => {
    if (state !== "unknown" || !eligible) return;
    if (firstLiveSeen(project, service)) {
      setState("hidden");
      return;
    }
    // Once: the next visit doesn't show it again.
    markFirstLiveSeen(project, service);
    setState("show");
  }, [eligible, state, project, service]);

  if (state !== "show" || !url) return null;
  const host = url.replace(/^https?:\/\//, "");
  return (
    <div
      role="status"
      className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-[var(--success)]/40 bg-[var(--success-subtle)] px-3 py-2"
    >
      <CheckCircle2 aria-hidden className="h-4 w-4 shrink-0 text-[var(--success)]" />
      <p className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 text-sm text-[var(--text-primary)]">
        <span className="font-medium">Live at</span>
        <a
          href={url}
          target="_blank"
          rel="noreferrer"
          className="min-w-0 max-w-full truncate font-mono underline-offset-4 hover:underline"
        >
          {host}
        </a>
        <CopyButton value={url} label="Copy URL" />
      </p>
      <div className="flex shrink-0 items-center gap-1.5">
        <Button size="sm" className="h-7 px-2.5 text-xs" render={<a href={url} target="_blank" rel="noreferrer" />} nativeButton={false}>
          <ExternalLink className="h-3 w-3" />
          Open
        </Button>
        <button
          type="button"
          onClick={() => setState("hidden")}
          aria-label="Dismiss"
          className="inline-flex h-7 w-7 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
        >
          <X className="h-4 w-4" />
        </button>
      </div>
    </div>
  );
}
