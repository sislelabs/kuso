"use client";

import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import { Send } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { SettingsSection } from "@/components/settings/SettingsSection";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import {
  lastDelivery,
  routeSummary,
  testEventType,
  testSummary,
  type AlertTestResult,
  type BuiltinAlertRow,
  type DeliverySummary,
} from "@/features/alerts";
import type { NotificationEventGroup } from "@/features/notifications";
import { toastError } from "@/lib/errors";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

const GROUP_LABEL: Record<NotificationEventGroup, string> = {
  build: "Build",
  runtime: "Runtime",
  jobs: "Jobs",
  nodes: "Nodes",
  backups: "Backups",
  other: "Other",
};

// BuiltinAlerts lists the events kuso alerts on without a rule, each with
// where it's delivered, its last delivery and a test send. Routing is
// edited on the channels (Settings → Notifications); this view is
// read-only so there's one place that decides who gets what.
export function BuiltinAlerts({
  rows,
  pending,
  error,
  onRetry,
}: {
  rows: BuiltinAlertRow[];
  pending: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  const groups = (Object.keys(GROUP_LABEL) as NotificationEventGroup[])
    .map((g) => ({ g, rows: rows.filter((r) => r.group === g) }))
    .filter((x) => x.rows.length > 0);
  const unrouted = rows.filter((r) => r.routing.routes.length === 0).length;
  return (
    <SettingsSection
      id="built-in"
      title="Built-in alerts"
      description={
        <>
          kuso sends these on its own; they need no rule and are always recorded in the bell feed. Which channels get each one is set
          on the channel under{" "}
          <Link href="/settings/notifications" className="text-[var(--text-primary)] underline underline-offset-2">
            Notifications
          </Link>
          .{unrouted > 0 && ` ${unrouted} of ${rows.length} reach no channel.`}
        </>
      }
      flush
    >
      {pending ? (
        <div className="p-4">
          <Skeleton className="h-24 w-full rounded-md" />
        </div>
      ) : error ? (
        <div className="p-4">
          <QueryErrorState what="built-in alerts" error={error} onRetry={onRetry} />
        </div>
      ) : (
        groups.map(({ g, rows: groupRows }) => (
          <div key={g}>
            <p className="border-b border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-4 py-1.5 text-xs font-medium text-[var(--text-secondary)] sm:px-5">
              {GROUP_LABEL[g]}
            </p>
            <ul className="divide-y divide-[var(--border-subtle)]">
              {groupRows.map((r) => (
                <BuiltinRow key={r.type} row={r} />
              ))}
            </ul>
          </div>
        ))
      )}
    </SettingsSection>
  );
}

function BuiltinRow({ row }: { row: BuiltinAlertRow }) {
  const routed = row.routing.routes.length > 0;
  const last = lastDelivery(row.deliveries);
  const test = useSendTest(row.label);
  return (
    <li className="flex flex-wrap items-start gap-x-3 gap-y-2 px-4 py-3 sm:flex-nowrap sm:px-5">
      <span
        aria-hidden
        title={routed ? "Delivered to a channel" : "Reaches no channel"}
        className={cn(
          "mt-1.5 inline-block h-2 w-2 shrink-0 rounded-full",
          routed ? "bg-[var(--success)]" : "border-[1.5px] border-[var(--text-secondary)] bg-transparent",
        )}
      />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <p className="text-sm font-medium text-[var(--text-primary)]">{row.label}</p>
          <span className="rounded-sm bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-xs text-[var(--text-secondary)]">Built-in</span>
          <span className="font-mono text-xs text-[var(--text-tertiary)]">{row.type}</span>
        </div>
        <p className={cn("mt-0.5 text-xs", routed ? "text-[var(--text-secondary)]" : "text-[var(--warning)]")}>
          {routed ? <>To {routeSummary(row.routing)}</> : routeSummary(row.routing)}
        </p>
        <DeliveryLine summary={last} />
        <TestResults result={test.result} />
      </div>
      <div className="flex w-full items-center justify-end gap-2 sm:w-auto">
        <Button
          variant="ghost"
          size="sm"
          disabled={!routed || test.pending}
          disabledReason={!routed ? "No channel receives this alert" : undefined}
          onClick={() => test.send(() => testEventType(row.type))}
          aria-label={`Send a test ${row.label} alert`}
        >
          <Send aria-hidden className="h-3.5 w-3.5" />
          {test.pending ? "Sending…" : "Send test"}
        </Button>
      </div>
    </li>
  );
}

// DeliveryLine shows the last delivery read from the delivery queue, or
// says none is recorded (the queue keeps a week; tests aren't recorded).
export function DeliveryLine({ summary }: { summary: DeliverySummary | null | undefined }) {
  if (summary === undefined) return null;
  if (summary === null) {
    return (
      <p className="mt-0.5 text-xs text-[var(--text-tertiary)]" title="Deliveries are kept for the notification retention window (7 days by default)">
        No delivery in the last week
      </p>
    );
  }
  return (
    <p className={cn("mt-0.5 text-xs", summary.ok ? "text-[var(--text-secondary)]" : "text-[var(--error)]")}>
      {summary.text} · {relativeTime(summary.at)}
    </p>
  );
}

export function TestResults({ result }: { result: AlertTestResult | null }) {
  if (!result) return null;
  return (
    <ul role="status" className="mt-1 space-y-0.5 text-xs">
      {result.results.length === 0 ? (
        <li className="text-[var(--text-secondary)]">{testSummary(result).text}</li>
      ) : (
        result.results.map((d) => (
          <li key={d.channel} className={d.ok ? "text-[var(--text-secondary)]" : "text-[var(--error)]"}>
            Test to {d.name} ({d.type}): {d.ok ? "delivered" : (d.error ?? "failed")}
          </li>
        ))
      )}
    </ul>
  );
}

// useSendTest runs one test send, toasts its outcome and keeps the
// per-channel results for the row.
export function useSendTest(what: string) {
  const [result, setResult] = useState<AlertTestResult | null>(null);
  const [pending, setPending] = useState(false);
  const send = async (fn: () => Promise<AlertTestResult>) => {
    setPending(true);
    try {
      const r = await fn();
      setResult(r);
      const sum = testSummary(r);
      if (sum.ok) toast.success(`Test ${what} alert sent`, { description: sum.text });
      else toast.error(`Test ${what} alert didn't reach every channel`, { description: sum.text });
    } catch (e) {
      toastError(e, "Couldn't send the test notification");
    } finally {
      setPending(false);
    }
  };
  return { result, pending, send };
}
