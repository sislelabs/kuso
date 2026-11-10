"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { Bell, BellOff, CheckCheck, Trash2 } from "lucide-react";
import { Segmented } from "@/components/ui/segmented";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { LoadingState } from "@/components/ui/loading-state";
import { useCan, Perms } from "@/features/auth";
import { useSystemVersion } from "@/features/system-version";
import { api } from "@/lib/api-client";
import { describeError, toastError } from "@/lib/errors";
import {
  bellRows,
  forgetLocalReads,
  markLocallyRead,
  setBellPrefs,
  unreadBellCount,
  useBellPrefs,
  useLocalReads,
  type BellView,
} from "@/lib/notification-attention";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { humanizeNotification } from "@/lib/format-notification";
import { servicePageHref } from "@/components/service/workspace/hrefs";
import { BELL_UNREAD_LIMIT, bellBadgeText, bellCount, bellUnreadLabel, type BellCount } from "./bellCount";

// FeedEvent matches the server's NotificationEvent wire shape. Only
// the fields the feed renders are typed.
interface FeedEvent {
  id: number;
  type: string;
  title: string;
  body?: string;
  severity?: string;
  project?: string;
  service?: string;
  url?: string;
  createdAt: string;
  readAt?: string | null;
  // alert.fired rows carry state "resolved" on the all-clear.
  extra?: Record<string, string>;
  // classification rides on failure events (build.failed, pod.crashed)
  // when the server's internal/failures package matched a known kind;
  // its summary says *why* something failed.
  classification?: {
    kind?: string;
    tab?: string;
    summary?: string;
    lineHint?: string;
    lineNum?: number;
  };
}

// useNotificationFeed owns the feed + unread queries and the mark-read /
// clear mutations, shared by the desktop popover and the mobile sheet.
// Admins get the full feed with read-tracking; everyone else gets a
// project-scoped read-only feed (/my-feed).
//
// Signal over noise: the default view and the badge cover only events
// that need attention (failures, alerts, security, updates). Successes
// live under "All activity" and only count toward the badge when the
// user opts in (Settings → Notifications).
function useNotificationFeed() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const prefs = useBellPrefs();
  const [open, setOpen] = useState(false);
  const qc = useQueryClient();
  // The badge polls the unread rows, not the server's count, so it runs
  // them through the same bellRows filter as the panel (the server count
  // also included update notices the panel hides once kuso is current).
  const unreadKey = ["notifications", "unread", prefs.countAll ? "all" : "attention"] as const;
  const unread = useQuery<FeedEvent[]>({
    queryKey: unreadKey,
    queryFn: () => api(`/api/notifications/feed?unread=true&limit=${BELL_UNREAD_LIMIT}${prefs.countAll ? "" : "&attention=true"}`),
    enabled: isAdmin,
    refetchInterval: 30_000,
    staleTime: 15_000,
    retry: false,
    throwOnError: false,
  });
  const view = prefs.view;
  const base = isAdmin ? "/api/notifications/feed" : "/api/notifications/my-feed";
  const feedPath = `${base}?limit=${FEED_LIMIT}${view === "attention" ? "&attention=true" : ""}`;
  // One const so the optimistic writes and the useQuery key can't drift.
  const feedKey = ["notifications", "feed", isAdmin ? "admin" : "scoped", view] as const;
  const feed = useQuery<FeedEvent[]>({
    queryKey: feedKey,
    queryFn: () => api(feedPath),
    enabled: open, // fetched while the feed is open
    staleTime: 15_000,
    retry: false,
    throwOnError: false,
  });
  const version = useSystemVersion();
  const localReads = useLocalReads();
  const items = useMemo(
    () => bellRows(feed.data ?? [], version.data?.needsUpdate, view === "attention"),
    [feed.data, version.data?.needsUpdate, view],
  );
  const locallyRead = useMemo(() => new Set(localReads.map((r) => r.id)), [localReads]);
  // Rows the server has since marked read no longer need a local entry.
  useEffect(() => {
    if (!feed.data) return;
    forgetLocalReads(new Set(feed.data.filter((e) => e.readAt).map((e) => e.id)));
  }, [feed.data]);

  // Optimistic: the badge drops to 0 and rows lose their unread styling
  // at once; both roll back if the server refuses.
  const markRead = useMutation({
    mutationFn: () => api("/api/notifications/feed/read-all", { method: "POST" }),
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: ["notifications", "unread"] });
      const prevUnread = qc.getQueryData<FeedEvent[]>(unreadKey);
      const prevFeed = qc.getQueryData<FeedEvent[]>(feedKey);
      const now = new Date().toISOString();
      qc.setQueriesData<FeedEvent[]>({ queryKey: ["notifications", "unread"] }, []);
      if (prevFeed) qc.setQueryData(feedKey, prevFeed.map((e) => (e.readAt ? e : { ...e, readAt: now })));
      return { prevUnread, prevFeed };
    },
    onSuccess: () => {
      forgetLocalReads();
    },
    onError: (e, _vars, ctx) => {
      if (ctx?.prevUnread) qc.setQueryData(unreadKey, ctx.prevUnread);
      if (ctx?.prevFeed) qc.setQueryData(feedKey, ctx.prevFeed);
      toastError(e, "Couldn't mark notifications read");
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["notifications", "unread"] });
      void qc.invalidateQueries({ queryKey: ["notifications", "feed"] });
    },
  });
  // Optimistically blank the feed, restore it on error.
  const clearAll = useMutation({
    mutationFn: () => api("/api/notifications/feed", { method: "DELETE" }),
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: feedKey });
      const previous = qc.getQueryData<FeedEvent[]>(feedKey);
      qc.setQueryData(feedKey, [] as FeedEvent[]);
      return { previous };
    },
    onSuccess: () => {
      forgetLocalReads();
      qc.invalidateQueries({ queryKey: ["notifications", "unread"] });
      qc.invalidateQueries({ queryKey: ["notifications", "feed"] });
    },
    onError: (e, _vars, ctx) => {
      toastError(e, "Couldn't clear notifications", { retry: () => clearAll.mutate() });
      if (ctx?.previous) qc.setQueryData(feedKey, ctx.previous);
    },
  });

  // Opening or closing the feed never marks anything read: a row is read
  // once it's opened, or after "Mark all read".
  const unreadCount = bellCount(
    unreadBellCount(unread.data ?? [], version.data?.needsUpdate, !prefs.countAll, locallyRead),
    unread.data?.length ?? 0,
  );
  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) void unread.refetch();
  };
  const isUnread = (e: FeedEvent) => isAdmin && !e.readAt && !locallyRead.has(e.id);
  return { isAdmin, open, feed, items, view, unreadCount, isUnread, clearAll, markRead, onOpenChange };
}

const FEED_LIMIT = 30;

type Feed = ReturnType<typeof useNotificationFeed>;

function FeedPanel({ state, onClose }: { state: Feed; onClose: () => void }) {
  const { isAdmin, feed, items, view, clearAll, markRead, unreadCount, isUnread } = state;
  const raw = feed.data ?? [];
  return (
    <>
      <header className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
        <div className="flex min-w-0 items-baseline gap-2">
          <p className="text-sm font-semibold tracking-tight">Notifications</p>
          {isAdmin && unreadCount.n > 0 && (
            <span className="text-xs text-[var(--text-secondary)] tabular-nums">{bellUnreadLabel(unreadCount)}</span>
          )}
        </div>
        <div className="flex items-center gap-1">
          {isAdmin && unreadCount.n > 0 && (
            <button
              type="button"
              onClick={() => markRead.mutate()}
              disabled={markRead.isPending}
              className="inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-50"
            >
              <CheckCheck className="size-3.5" aria-hidden />
              Mark all read
            </button>
          )}
          {isAdmin && (
            <Link
              href="/settings/notifications"
              onClick={onClose}
              className="inline-flex h-7 items-center rounded-md px-2 text-xs text-[var(--link)] hover:bg-[var(--bg-tertiary)]"
            >
              Settings
            </Link>
          )}
        </div>
      </header>
      <div className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] px-3 py-1.5">
        <Segmented
          size="sm"
          label="Show"
          value={view}
          onChange={(v: BellView) => setBellPrefs({ view: v })}
          options={[
            { value: "attention", label: "Needs attention" },
            { value: "all", label: "All activity" },
          ]}
        />
        {isAdmin && raw.length > 0 && (
          <button
            type="button"
            onClick={() => clearAll.mutate()}
            disabled={clearAll.isPending}
            title="Delete every notification in the feed, for every admin"
            className="inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-50"
          >
            <Trash2 className="size-3.5" aria-hidden />
            Clear all
          </button>
        )}
      </div>
      {/* relative: the sr-only "New:" span on each unread row is absolute;
          without a positioned scroller it escapes the clip and stretches
          the page under a long feed (a page scrollbar while open). */}
      <div className="relative max-h-[min(28rem,70dvh)] overflow-y-auto">
        {feed.isPending ? (
          <LoadingState kind="inline" className="px-3 py-4" />
        ) : feed.isError ? (
          <div role="alert" className="flex items-start justify-between gap-3 px-3 py-4">
            <p className="text-sm text-[var(--error)]">{describeError(feed.error, "Couldn't load notifications.")}</p>
            <button
              type="button"
              onClick={() => void feed.refetch()}
              className="shrink-0 text-xs font-medium text-[var(--link)] hover:underline"
            >
              Retry
            </button>
          </div>
        ) : items.length === 0 ? (
          <div className="flex flex-col items-center px-6 py-10 text-center">
            <BellOff aria-hidden className="mb-2 size-5 text-[var(--text-tertiary)]" />
            <p className="text-sm font-medium text-[var(--text-primary)]">
              {view === "attention" ? "Nothing needs your attention" : "No activity yet"}
            </p>
            <p className="mt-1 text-xs text-[var(--text-tertiary)]">
              {view === "attention"
                ? "Failed builds, crashes, alerts, security findings and updates show up here."
                : "Builds, deploys and node events show up here as they happen."}
            </p>
            {view === "attention" && (
              <button
                type="button"
                onClick={() => setBellPrefs({ view: "all" })}
                className="mt-3 text-xs font-medium text-[var(--link)] hover:underline"
              >
                Show all activity
              </button>
            )}
          </div>
        ) : (
          <ul className="divide-y divide-[var(--border-subtle)]">
            {items.map((e) => (
              <NotificationRow key={e.id} event={e} unread={isUnread(e)} onClose={onClose} />
            ))}
          </ul>
        )}
        {raw.length >= FEED_LIMIT && (
          <p className="border-t border-[var(--border-subtle)] px-3 py-2 text-xs text-[var(--text-tertiary)]">
            Showing the {FEED_LIMIT} most recent.
          </p>
        )}
      </div>
    </>
  );
}

function BellIcon({ unread }: { unread: BellCount }) {
  return (
    <>
      <Bell className="size-4" aria-hidden />
      {unread.n > 0 && (
        <span
          aria-hidden
          className="absolute -top-1 -right-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-[var(--text-primary)] px-1 text-2xs leading-none font-semibold tabular-nums text-[var(--bg-primary)] ring-2 ring-[var(--bg-primary)]"
        >
          {bellBadgeText(unread)}
        </span>
      )}
    </>
  );
}

export function NotificationsButton({ className }: { className?: string }) {
  const state = useNotificationFeed();
  // Controlled so a row's <Link> click closes the popover before routing.
  const { open, onOpenChange } = state;
  const count = state.unreadCount;
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger
        aria-label={count.n > 0 ? `Notifications, ${bellUnreadLabel(count)}` : "Notifications"}
        title="Notifications"
        className={cn(
          "relative inline-flex size-8 items-center justify-center rounded-md text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] data-[popup-open]:bg-[var(--bg-tertiary)]",
          className,
        )}
      >
        <BellIcon unread={count} />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-96 max-w-[calc(100vw-1rem)] gap-0 p-0">
        <FeedPanel state={state} onClose={() => onOpenChange(false)} />
      </PopoverContent>
    </Popover>
  );
}

// Mobile: the same feed in a bottom sheet, opened from the bottom bar.
export function NotificationsSheetButton({ className, children }: { className?: string; children?: React.ReactNode }) {
  const state = useNotificationFeed();
  const { open, onOpenChange } = state;
  const count = state.unreadCount;
  return (
    <>
      <button
        type="button"
        onClick={() => onOpenChange(true)}
        aria-label={count.n > 0 ? `Notifications, ${bellUnreadLabel(count)}` : "Notifications"}
        className={className}
      >
        <span className="relative inline-flex size-6 items-center justify-center [&_svg]:size-5">
          <BellIcon unread={count} />
        </span>
        {children}
      </button>
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent side="bottom" showCloseButton={false} className="gap-0 rounded-t-xl pb-[env(safe-area-inset-bottom)]">
          <SheetTitle className="sr-only">Notifications</SheetTitle>
          <FeedPanel state={state} onClose={() => onOpenChange(false)} />
        </SheetContent>
      </Sheet>
    </>
  );
}

// Server titles carry a status glyph for chat channels ("✓ Build
// succeeded"); the row's dot already says that.
// NotificationRow is one feed entry. Rows with a target (server url, or a
// project/service fallback for pre-v0.8.2 events) are links that close
// the feed before navigating; others are plain rows.
function NotificationRow({
  event,
  unread = false,
  onClose,
}: {
  event: FeedEvent;
  unread?: boolean;
  onClose: () => void;
}) {
  const where = [event.project, event.service].filter(Boolean).join("/");
  const text = humanizeNotification(event);
  const body = (
    <div className="flex items-start gap-2.5">
      <span
        aria-hidden
        className={cn(
          "mt-1.5 inline-block size-2 shrink-0 rounded-full",
          event.severity === "error"
            ? "bg-[var(--error)]"
            : event.severity === "warn"
              ? "bg-[var(--warning)]"
              : "bg-[var(--success)]",
        )}
      />
      <div className="min-w-0 flex-1">
        <p className={cn("truncate text-[13px] text-[var(--text-primary)]", unread ? "font-semibold" : "font-medium")}>
          {unread && <span className="sr-only">New: </span>}
          {text.title}
        </p>
        {text.body.length > 0 && (
          <p className="mt-0.5 line-clamp-2 text-xs text-[var(--text-secondary)]">
            {text.body.map((p, i) =>
              p.code ? (
                <code key={i} className="rounded bg-[var(--bg-tertiary)] px-1 font-mono">
                  {p.text}
                </code>
              ) : (
                <span key={i}>{p.text}</span>
              ),
            )}
          </p>
        )}
        <p className="mt-1 flex flex-wrap items-center gap-x-2 text-xs text-[var(--text-tertiary)]">
          {where && <span className="font-mono">{where}</span>}
          <span>{relativeTime(event.createdAt)}</span>
        </p>
      </div>
      {unread && <span aria-hidden className="mt-1.5 size-1.5 shrink-0 rounded-full bg-[var(--accent)]" />}
    </div>
  );

  const href =
    event.url ||
    (event.project && event.service
      ? servicePageHref(event.project, event.service)
      : event.project
        ? `/projects/${encodeURIComponent(event.project)}`
        : "");

  if (href) {
    return (
      <li className="transition-colors hover:bg-[var(--bg-tertiary)]/60">
        <Link
          href={href}
          onClick={() => {
            if (unread) markLocallyRead(event);
            onClose();
          }}
          className="block px-3 py-2.5 focus-visible:outline-offset-[-2px]"
        >
          {body}
        </Link>
      </li>
    );
  }
  // Nothing to open: the row itself marks it read.
  if (unread) {
    return (
      <li className="transition-colors hover:bg-[var(--bg-tertiary)]/60">
        <button
          type="button"
          onClick={() => markLocallyRead(event)}
          title="Mark as read"
          className="block w-full px-3 py-2.5 text-left focus-visible:outline-offset-[-2px]"
        >
          {body}
        </button>
      </li>
    );
  }
  return <li className="px-3 py-2.5">{body}</li>;
}
