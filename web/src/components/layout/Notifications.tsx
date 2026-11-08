"use client";

import Link from "next/link";
import { useState } from "react";
import { Bell, BellOff, CheckCheck, Trash2 } from "lucide-react";
import { Segmented } from "@/components/ui/segmented";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { LoadingState } from "@/components/ui/loading-state";
import { useCan, Perms } from "@/features/auth";
import { api } from "@/lib/api-client";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

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
function useNotificationFeed() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const qc = useQueryClient();
  const unread = useQuery<{ unread: number }>({
    queryKey: ["notifications", "unread-count"],
    queryFn: () => api("/api/notifications/feed/unread-count"),
    enabled: isAdmin,
    refetchInterval: 30_000,
    staleTime: 15_000,
    retry: false,
    throwOnError: false,
  });
  const feedPath = isAdmin ? `/api/notifications/feed?limit=${FEED_LIMIT}` : `/api/notifications/my-feed?limit=${FEED_LIMIT}`;
  // One const so the optimistic-clear write and the useQuery key can't drift.
  const feedKey = ["notifications", "feed", isAdmin ? "admin" : "scoped"] as const;
  const feed = useQuery<FeedEvent[]>({
    queryKey: feedKey,
    queryFn: () => api(feedPath),
    enabled: false, // fetched when the feed is opened
    staleTime: 15_000,
    retry: false,
    throwOnError: false,
  });
  // Optimistic: the badge drops to 0 and rows lose their unread styling
  // at once; both roll back if the server refuses.
  const markRead = useMutation({
    mutationFn: () => api("/api/notifications/feed/read-all", { method: "POST" }),
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: ["notifications", "unread-count"] });
      const prevCount = qc.getQueryData<{ unread: number }>(["notifications", "unread-count"]);
      const prevFeed = qc.getQueryData<FeedEvent[]>(feedKey);
      const now = new Date().toISOString();
      qc.setQueryData(["notifications", "unread-count"], { unread: 0 });
      if (prevFeed) qc.setQueryData(feedKey, prevFeed.map((e) => (e.readAt ? e : { ...e, readAt: now })));
      return { prevCount, prevFeed };
    },
    onError: (e, _vars, ctx) => {
      if (ctx?.prevCount) qc.setQueryData(["notifications", "unread-count"], ctx.prevCount);
      if (ctx?.prevFeed) qc.setQueryData(feedKey, ctx.prevFeed);
      toast.error(e instanceof Error ? `Couldn't mark notifications read: ${e.message}` : "Couldn't mark notifications read");
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["notifications", "unread-count"] });
    },
  });
  // Optimistically blank the feed, restore it on error (the feed query is
  // enabled:false, so an invalidate alone wouldn't refetch).
  const clearAll = useMutation({
    mutationFn: () => api("/api/notifications/feed", { method: "DELETE" }),
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: feedKey });
      const previous = qc.getQueryData<FeedEvent[]>(feedKey);
      qc.setQueryData(feedKey, [] as FeedEvent[]);
      return { previous };
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["notifications", "unread-count"] });
      qc.invalidateQueries({ queryKey: feedKey });
    },
    onError: (e, _vars, ctx) => {
      toast.error(e instanceof Error ? `Couldn't clear notifications: ${e.message}` : "Couldn't clear notifications");
      if (ctx?.previous) qc.setQueryData(feedKey, ctx.previous);
    },
  });

  const unreadCount = unread.data?.unread ?? 0;
  const onOpenChange = (next: boolean) => {
    if (next) {
      void feed.refetch();
      void unread.refetch();
    } else if (isAdmin && unreadCount > 0) {
      // Seen = read, on CLOSE, not open: marking on open cleared readAt
      // before the feed rendered, so nothing could show as new.
      markRead.mutate();
    }
  };
  return { isAdmin, feed, unreadCount, clearAll, markRead, onOpenChange };
}

const FEED_LIMIT = 30;

type Feed = ReturnType<typeof useNotificationFeed>;

function FeedPanel({ state, onClose }: { state: Feed; onClose: () => void }) {
  const { isAdmin, feed, clearAll, markRead, unreadCount } = state;
  const [filter, setFilter] = useState<"all" | "unread">("all");
  const all = feed.data ?? [];
  const items = filter === "unread" ? all.filter((e) => !e.readAt) : all;
  return (
    <>
      <header className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
        <div className="flex min-w-0 items-baseline gap-2">
          <p className="text-sm font-semibold tracking-tight">Notifications</p>
          {isAdmin && unreadCount > 0 && (
            <span className="text-xs text-[var(--text-secondary)] tabular-nums">{unreadCount} unread</span>
          )}
        </div>
        <div className="flex items-center gap-1">
          {isAdmin && unreadCount > 0 && (
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
              Channels
            </Link>
          )}
        </div>
      </header>
      {isAdmin && all.length > 0 && (
        <div className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] px-3 py-1.5">
          <Segmented
            size="sm"
            label="Show"
            value={filter}
            onChange={setFilter}
            options={[
              { value: "all", label: "All" },
              { value: "unread", label: "Unread" },
            ]}
          />
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
        </div>
      )}
      <div className="max-h-[min(28rem,70dvh)] overflow-y-auto">
        {feed.isPending ? (
          <LoadingState kind="inline" className="px-3 py-4" />
        ) : feed.isError ? (
          <p role="alert" className="px-3 py-4 text-sm text-[var(--error)]">
            Couldn&apos;t load notifications: {feed.error instanceof Error ? feed.error.message : "unknown error"}
          </p>
        ) : items.length === 0 ? (
          <div className="flex flex-col items-center px-6 py-10 text-center">
            <BellOff aria-hidden className="mb-2 size-5 text-[var(--text-tertiary)]" />
            <p className="text-sm font-medium text-[var(--text-primary)]">
              {filter === "unread" ? "No unread notifications" : "You\u2019re all caught up"}
            </p>
            <p className="mt-1 text-xs text-[var(--text-tertiary)]">
              Builds, deploys and node alerts show up here as they happen.
            </p>
          </div>
        ) : (
          <ul className="divide-y divide-[var(--border-subtle)]">
            {items.map((e) => (
              <NotificationRow key={e.id} event={e} unread={isAdmin && !e.readAt} onClose={onClose} />
            ))}
          </ul>
        )}
        {all.length >= FEED_LIMIT && filter === "all" && (
          <p className="border-t border-[var(--border-subtle)] px-3 py-2 text-xs text-[var(--text-tertiary)]">
            Showing the {FEED_LIMIT} most recent.
            {unreadCount > all.filter((e) => !e.readAt).length && " Older unread ones are cleared by Mark all read."}
          </p>
        )}
      </div>
    </>
  );
}

function BellIcon({ unread }: { unread: number }) {
  return (
    <>
      <Bell className="size-4" aria-hidden />
      {unread > 0 && (
        <span
          aria-hidden
          className="absolute -top-0.5 -right-0.5 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-[var(--text-primary)] px-1 text-2xs leading-none font-semibold tabular-nums text-[var(--bg-primary)]"
        >
          {unread > 99 ? "99+" : unread}
        </span>
      )}
    </>
  );
}

export function NotificationsButton({ className }: { className?: string }) {
  const state = useNotificationFeed();
  // Controlled so a row's <Link> click closes the popover before routing.
  const [open, setOpen] = useState(false);
  const onOpenChange = (next: boolean) => {
    setOpen(next);
    state.onOpenChange(next);
  };
  const n = state.unreadCount;
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger
        aria-label={n > 0 ? `Notifications, ${n} unread` : "Notifications"}
        title="Notifications"
        className={cn(
          "relative inline-flex size-8 items-center justify-center rounded-md text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] data-[popup-open]:bg-[var(--bg-tertiary)]",
          className,
        )}
      >
        <BellIcon unread={n} />
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
  const [open, setOpen] = useState(false);
  const onOpenChange = (next: boolean) => {
    setOpen(next);
    state.onOpenChange(next);
  };
  const n = state.unreadCount;
  return (
    <>
      <button
        type="button"
        onClick={() => onOpenChange(true)}
        aria-label={n > 0 ? `Notifications, ${n} unread` : "Notifications"}
        className={className}
      >
        <span className="relative inline-flex size-6 items-center justify-center [&_svg]:size-5">
          <BellIcon unread={n} />
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
function feedTitle(title: string): string {
  return title.replace(/^[\s\u2713\u2714\u2717\u2718\u26A0\uFE0F\u274C\u2705\u{1F534}\u{1F7E2}\u{1F7E1}]+/u, "");
}

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
          {feedTitle(event.title)}
        </p>
        {(event.classification?.summary || event.body) && (
          <p className="mt-0.5 line-clamp-2 text-xs text-[var(--text-secondary)]">
            {event.classification?.summary || event.body}
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
      ? `/projects/${encodeURIComponent(event.project)}?service=${encodeURIComponent(event.service)}`
      : event.project
        ? `/projects/${encodeURIComponent(event.project)}`
        : "");

  if (href) {
    return (
      <li className="transition-colors hover:bg-[var(--bg-tertiary)]/60">
        <Link href={href} onClick={onClose} className="block px-3 py-2.5 focus-visible:outline-offset-[-2px]">
          {body}
        </Link>
      </li>
    );
  }
  return <li className="px-3 py-2.5">{body}</li>;
}
