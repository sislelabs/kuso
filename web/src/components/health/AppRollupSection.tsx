"use client";

import { useState, type ComponentType, type ReactNode } from "react";
import Link from "next/link";
import {
  Activity,
  AlertTriangle,
  Boxes,
  Bug,
  CheckCircle2,
  ChevronRight,
  Database,
  OctagonAlert,
} from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import { StatusPill } from "@/components/ui/status-pill";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { relativeTime } from "@/lib/format";
import { TONE_CLASSES, type StatusMeta, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";
import { verdict, type AppRollup, type ReconcileCounts } from "./appRollup";
import { useAppRollup } from "./useAppRollup";

const VISIBLE_ITEMS = 5;

const VERDICT_ICON: Record<"success" | "warning" | "danger", ComponentType<{ className?: string }>> = {
  success: CheckCircle2,
  warning: AlertTriangle,
  danger: OctagonAlert,
};

// AppRollupSection answers "is everything I run OK?" across every
// project: one verdict line, then a row per signal linking to where you
// act on it. reconcile folds the scan below into the verdict so
// the page has a single success state.
export function AppRollupSection({ reconcile }: { reconcile?: ReconcileCounts }) {
  const { rollup, probesPending, isPending, error, refetch } = useAppRollup();

  if (isPending) {
    return (
      <section aria-label="App health" className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 sm:p-5">
        <Skeleton className="h-6 w-64" />
        <Skeleton className="mt-2 h-4 w-80 max-w-full" />
        <div className="mt-5 space-y-3">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      </section>
    );
  }
  if (error || !rollup) {
    return <QueryErrorState what="app health" error={error} onRetry={refetch} />;
  }

  return (
    <section
      aria-labelledby="app-health-verdict"
      className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
    >
      <VerdictHeader rollup={rollup} pending={probesPending} reconcile={reconcile} />
      <ul className="divide-y divide-[var(--border-subtle)] border-t border-[var(--border-subtle)]">
        <ServicesRow rollup={rollup} />
        <UptimeRow rollup={rollup} pending={probesPending} />
        {(probesPending || rollup.errors.supported) && <ErrorsRow rollup={rollup} pending={probesPending} />}
        <AddonsRow rollup={rollup} pending={probesPending} />
      </ul>
      {!probesPending && !rollup.errors.supported && (
        <p className="border-t border-[var(--border-subtle)] px-4 py-2.5 text-xs text-[var(--text-tertiary)] sm:px-5">
          Error counts aren&apos;t shown: this kuso server is too old to report them.
        </p>
      )}
    </section>
  );
}

function VerdictHeader({
  rollup,
  pending,
  reconcile,
}: {
  rollup: AppRollup;
  pending: boolean;
  reconcile?: ReconcileCounts;
}) {
  const s = rollup.services;
  const facts = [
    `${s.running} running`,
    s.inFlight > 0 && `${s.inFlight} deploying`,
    s.sleeping > 0 && `${s.sleeping} asleep`,
    s.stopped > 0 && `${s.stopped} stopped`,
    s.notDeployed > 0 && `${s.notDeployed} not deployed`,
  ].filter(Boolean);

  // Hold the verdict until the probes land so it never flips from
  // "healthy" to "failing" a second after first paint.
  const v = pending ? null : verdict(rollup, reconcile);
  const Icon = v ? VERDICT_ICON[v.tone] : CheckCircle2;
  return (
    <header className="flex items-start gap-3 px-4 py-4 sm:px-5">
      <span
        aria-hidden
        className={cn(
          "mt-0.5 inline-flex size-8 shrink-0 items-center justify-center rounded-md",
          v ? cn(TONE_CLASSES[v.tone].chip, "border") : "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)]",
        )}
      >
        <Icon className="size-4" />
      </span>
      <div className="min-w-0">
        {v ? (
          <h2 id="app-health-verdict" className="text-base font-semibold tracking-tight text-[var(--text-primary)]">
            {v.headline}
          </h2>
        ) : (
          <h2 id="app-health-verdict" className="sr-only">
            Checking app health
          </h2>
        )}
        {!v && <Skeleton aria-hidden className="h-5 w-56" />}
        <p className="mt-0.5 text-sm text-[var(--text-secondary)]">
          {s.total === 0 ? "Nothing deployed yet." : `${facts.join(" · ")} across all projects`}
        </p>
      </div>
    </header>
  );
}

function RollupRow({
  icon: Icon,
  label,
  hint,
  summary,
  tone,
  pending,
  children,
  footnote,
}: {
  icon: ComponentType<{ className?: string }>;
  label: string;
  hint: string;
  summary: ReactNode;
  // Tone of the summary; neutral when nothing needs doing.
  tone: StatusTone;
  pending?: boolean;
  children?: ReactNode;
  footnote?: ReactNode;
}) {
  return (
    <li className="px-4 py-3 sm:px-5">
      <div className="flex items-center gap-3">
        <Icon className="size-4 shrink-0 text-[var(--text-tertiary)]" />
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium text-[var(--text-primary)]">{label}</div>
          <div className="text-xs text-[var(--text-secondary)]">{hint}</div>
        </div>
        {pending ? (
          <Skeleton className="h-4 w-20" />
        ) : (
          <div className={cn("shrink-0 text-right text-sm tabular-nums", TONE_CLASSES[tone].text)}>{summary}</div>
        )}
      </div>
      {!pending && children}
      {!pending && footnote && <p className="mt-2 pl-7 text-xs text-[var(--text-tertiary)]">{footnote}</p>}
    </li>
  );
}

// ItemList keeps a long incident short: the first few, then a toggle.
function ItemList<T>({ items, render }: { items: T[]; render: (item: T) => ReactNode }) {
  const [all, setAll] = useState(false);
  if (items.length === 0) return null;
  const shown = all ? items : items.slice(0, VISIBLE_ITEMS);
  const hidden = items.length - shown.length;
  return (
    <div className="mt-2 sm:pl-7">
      <ul className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]">
        {shown.map((item, i) => (
          <li key={i} className="border-t border-[var(--border-subtle)] first:border-t-0">
            {render(item)}
          </li>
        ))}
      </ul>
      {(hidden > 0 || all) && items.length > VISIBLE_ITEMS && (
        <button
          type="button"
          onClick={() => setAll((v) => !v)}
          className="mt-1.5 rounded-sm text-xs font-medium text-[var(--text-secondary)] hover:text-[var(--text-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--focus)]"
        >
          {all ? "Show fewer" : `Show ${hidden} more`}
        </button>
      )}
    </div>
  );
}

function ItemLink({
  href,
  status,
  name,
  tag,
  detail,
  aside,
}: {
  // Without one the row is plain text (e.g. errors of a deleted service).
  href?: string;
  status?: StatusMeta;
  name: string;
  tag?: string;
  detail?: string;
  aside?: ReactNode;
}) {
  const body = (
    <>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5 sm:flex-row sm:items-center sm:gap-2">
        <span className="flex min-w-0 items-center gap-2">
          {status && <StatusPill status={status} variant="tinted" detail={detail} />}
          <span className="truncate font-medium text-[var(--text-primary)]">{name}</span>
          {tag && (
            <span className="shrink-0 rounded-sm bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-xs text-[var(--text-secondary)]">
              {tag}
            </span>
          )}
        </span>
        {detail && <span className="min-w-0 truncate text-xs text-[var(--text-secondary)]">{detail}</span>}
      </div>
      {aside && <span className="shrink-0 text-xs text-[var(--text-secondary)] tabular-nums">{aside}</span>}
    </>
  );
  if (!href) return <div className="flex items-center gap-2 px-3 py-2 pr-9 text-sm">{body}</div>;
  return (
    <Link
      href={href}
      className="group flex items-center gap-2 px-3 py-2 text-sm hover:bg-[var(--bg-tertiary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--focus)]"
    >
      {body}
      <ChevronRight className="size-4 shrink-0 text-[var(--text-tertiary)] group-hover:text-[var(--text-secondary)]" />
    </Link>
  );
}

function unavailableNote(projects: string[], what: string): string | undefined {
  if (projects.length === 0) return undefined;
  const names = projects.length <= 3 ? `: ${projects.join(", ")}` : "";
  return `Couldn't read ${what} for ${projects.length} project${projects.length === 1 ? "" : "s"}${names}.`;
}

function ServicesRow({ rollup }: { rollup: AppRollup }) {
  const { problems, running } = rollup.services;
  return (
    <RollupRow
      icon={Boxes}
      label="Services"
      hint="Crash-looping, failed deploys or not all replicas ready"
      tone={problems.some((p) => p.status.tone === "danger") ? "danger" : problems.length ? "warning" : "neutral"}
      summary={problems.length ? `${problems.length} need${problems.length === 1 ? "s" : ""} attention` : `${running} running`}
    >
      <ItemList
        items={problems}
        render={(p) => (
          <ItemLink
            href={p.href}
            status={p.status}
            name={`${p.project} / ${p.service}`}
            tag={p.group === "production" ? undefined : p.group}
            detail={[p.cause, p.detail].filter(Boolean).join(" · ") || undefined}
          />
        )}
      />
    </RollupRow>
  );
}

function UptimeRow({ rollup, pending }: { rollup: AppRollup; pending: boolean }) {
  const u = rollup.uptime;
  const off = u.enabled === false;
  return (
    <RollupRow
      icon={Activity}
      label="Uptime checks"
      hint="Production URLs pinged from inside the cluster"
      pending={pending}
      tone={u.problems.some((p) => p.status.tone === "danger") ? "danger" : u.problems.length ? "warning" : "neutral"}
      summary={
        off
          ? "Off"
          : u.problems.length
            ? `${u.problems.length} failing`
            : u.checks === 0
              ? "No checks"
              : [`${u.up} up`, u.paused > 0 && `${u.paused} paused`, u.pending > 0 && `${u.pending} pending`]
                  .filter(Boolean)
                  .join(" · ")
      }
      footnote={off ? "Uptime checks are switched off for this instance." : unavailableNote(u.unavailable, "uptime")}
    >
      <ItemList
        items={u.problems}
        render={(p) => (
          <ItemLink
            href={p.href}
            status={p.status}
            name={`${p.project} / ${p.service}`}
            detail={[p.detail, p.stale ? "last check is stale" : undefined].filter(Boolean).join(" · ") || undefined}
            aside={p.since ? `since ${relativeTime(p.since)}` : undefined}
          />
        )}
      />
    </RollupRow>
  );
}

function ErrorsRow({ rollup, pending }: { rollup: AppRollup; pending: boolean }) {
  const e = rollup.errors;
  return (
    <RollupRow
      icon={Bug}
      label="Errors · 24h"
      hint="Occurrences of runtime error groups in the last 24 hours"
      pending={pending}
      tone={e.occurrences > 0 ? "warning" : "neutral"}
      summary={
        e.occurrences > 0
          ? `${e.occurrences.toLocaleString()} error${e.occurrences === 1 ? "" : "s"} · 24h`
          : "None"
      }
      footnote={unavailableNote(e.unavailable, "errors")}
    >
      <ItemList
        items={e.hotspots}
        render={(h) => (
          <ItemLink
            href={h.href}
            name={`${h.project} / ${h.service}`}
            tag={h.href ? undefined : "deleted"}
            detail={`${h.groups} group${h.groups === 1 ? "" : "s"}${h.lastSeen ? ` · last ${relativeTime(h.lastSeen)}` : ""}`}
            aside={`${h.occurrences.toLocaleString()} error${h.occurrences === 1 ? "" : "s"}`}
          />
        )}
      />
    </RollupRow>
  );
}

function AddonsRow({ rollup, pending }: { rollup: AppRollup; pending: boolean }) {
  const a = rollup.addons;
  const managed = a.total - a.external;
  const failing = a.problems.filter((p) => p.status.tone === "danger").length;
  return (
    <RollupRow
      icon={Database}
      label="Addons"
      hint="Databases, caches and queues whose release isn't deployed"
      pending={pending}
      tone={failing > 0 ? "danger" : a.problems.length ? "progress" : "neutral"}
      summary={
        a.problems.length
          ? `${a.problems.length} not live`
          : managed === 0
            ? "None"
            : `${a.live} of ${managed} live`
      }
      footnote={unavailableNote(a.unavailable, "addons")}
    >
      <ItemList
        items={a.problems}
        render={(p) => (
          <ItemLink href={p.href} status={p.status} name={`${p.project} / ${p.addon}`} detail={p.detail} />
        )}
      />
    </RollupRow>
  );
}
