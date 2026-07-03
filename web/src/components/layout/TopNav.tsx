"use client";

import Link from "next/link";
import { useRouter, usePathname, useSearchParams } from "next/navigation";
import { Fragment } from "react";
import { useMemo, useState } from "react";
import {
  Avatar,
  AvatarFallback,
  AvatarImage,
} from "@/components/ui/avatar";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { Logo } from "@/components/shared/Logo";
import { ThemeToggle } from "@/components/shared/ThemeToggle";
import { useSession, useSignOut, useCan, Perms } from "@/features/auth";
import { useProjects, useEnvGroups } from "@/features/projects";
import { useRouteParams } from "@/lib/dynamic-params";
import { cn } from "@/lib/utils";
import {
  Check,
  ChevronDown,
  ChevronRight,
  LogOut,
  Plus,
  User as UserIcon,
  KeyRound,
  Bell,
  Settings,
  Users,
  Trash2,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { ServersPopover } from "./ServersPopover";
import { NewEnvironmentDialog } from "./NewEnvironmentDialog";

// TopNav is the persistent shell across every authenticated page. Left
// side: kuso logomark, then breadcrumb-style project picker + (when on
// a project) the environment switcher. Right side: servers, theme,
// notifications, user menu. The whole bar is `h-12 sticky` to leave
// room below for the page's own toolbar.
export function TopNav() {
  const params = useRouteParams<{ project: string }>(["project"]);
  const currentProject = params.project ?? "";
  const pathname = usePathname() ?? "";
  const settingsCrumbs = pathname.startsWith("/settings/")
    ? settingsBreadcrumb(pathname)
    : null;

  return (
    <header
      role="navigation"
      className="sticky top-0 z-40 flex h-12 shrink-0 items-center gap-1 border-b border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 sm:gap-1.5 sm:px-3 lg:px-4"
    >
      <Link href="/projects" aria-label="Home" className="mr-1 flex shrink-0 items-center sm:mr-1.5">
        <Logo />
      </Link>

      {/* Project breadcrumb is suppressed on /settings/* — it'd lie
          about a project being in scope, and the env switcher would
          render meaningless options. */}
      {!settingsCrumbs && (
        <Crumb>
          <ProjectPicker currentProject={currentProject} />
        </Crumb>
      )}

      {/* Env switcher is redundant on phones — the project canvas
          already shows the env state per service node, and the
          drawer eats too much horizontal room next to the picker.
          Hide on small; bring back at sm. */}
      {currentProject && !settingsCrumbs && (
        <Crumb className="hidden sm:flex">
          <EnvironmentSwitcher project={currentProject} />
        </Crumb>
      )}

      {/* Per-project Settings affordance. Before this the page
          existed (/projects/<name>/settings) but was reachable only
          via URL-typing or cmd-K — destructive actions like Delete
          project and Preview TTL had no visible path from the
          canvas. The cog sits at the end of the breadcrumb so it
          reads as "settings for the thing in scope". */}
      {currentProject && !settingsCrumbs && (
        <Link
          href={`/projects/${encodeURIComponent(currentProject)}/settings`}
          aria-label="Project settings"
          title="Project settings"
          className="hidden sm:inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
        >
          <Settings className="h-3.5 w-3.5" />
        </Link>
      )}

      {settingsCrumbs?.map((c, i) => (
        <Fragment key={c.href ?? c.label}>
          <Crumb className={i === 0 ? "hidden sm:flex" : undefined}>
            {c.href ? (
              <Link
                href={c.href}
                className="inline-flex h-7 items-center gap-1.5 rounded-md px-2 text-sm font-medium text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                {c.label}
              </Link>
            ) : (
              <span className="inline-flex h-7 items-center px-2 text-sm font-medium text-[var(--text-primary)] truncate max-w-[180px] sm:max-w-none">
                {c.label}
              </span>
            )}
          </Crumb>
          {void i}
        </Fragment>
      ))}

      <div className="flex-1" />

      {/* Mobile: icon-only Settings to save horizontal pixels. The
          full-text version returns at sm. ServersPopover renders an
          icon either way; ThemeToggle is icon-only. Marketplace lives
          in the create-project flow, not the global nav. */}
      <Link
        href="/settings"
        aria-label="Settings"
        className="inline-flex h-7 items-center gap-1.5 rounded-md px-2 text-xs font-medium text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
      >
        <Settings className="h-3.5 w-3.5" />
        <span className="hidden sm:inline">Settings</span>
      </Link>
      <div className="hidden sm:flex">
        <ServersPopover />
      </div>
      <ThemeToggle />
      <NotificationsButton />
      <UserMenu />
    </header>
  );
}

// settingsBreadcrumb returns the crumbs to render for /settings/* paths.
// "Settings" is a link back to the index; the trailing crumb is the
// current section (Profile, Tokens, Cluster nodes, etc.) and is not a
// link because it's where we already are.
function settingsBreadcrumb(pathname: string): { label: string; href?: string }[] {
  const segs = pathname.replace(/^\/+|\/+$/g, "").split("/");
  // segs[0] === "settings"; segs[1] is the section.
  const section = segs[1];
  const labels: Record<string, string> = {
    profile: "Profile",
    tokens: "API tokens",
    notifications: "Notifications",
    nodes: "Cluster nodes",
    config: "Cluster config",
    users: "Users",
    groups: "Groups",
  };
  const out: { label: string; href?: string }[] = [
    { label: "Settings", href: "/settings" },
  ];
  if (section && labels[section]) {
    out.push({ label: labels[section] });
  }
  return out;
}

// Crumb wraps a child with a thin chevron separator on its left.
// Renders nothing when no children are provided so empty slots don't
// leave dangling separators.
function Crumb({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  // The chevron + child render as a contiguous unit, so a single
  // wrapping span is necessary when callers need to hide just this
  // crumb (mobile responsiveness). The original fragment-only
  // version stripped the className escape hatch.
  return (
    <span className={cn("flex shrink-0 items-center gap-1", className)}>
      <ChevronRight className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]/60" />
      {children}
    </span>
  );
}

function ProjectPicker({ currentProject }: { currentProject: string }) {
  const projects = useProjects();
  const router = useRouter();
  const [open, setOpen] = useState(false);

  const sorted = useMemo(() => {
    const list = projects.data ?? [];
    return [...list].sort((a, b) => a.metadata.name.localeCompare(b.metadata.name));
  }, [projects.data]);

  const label = currentProject || "Projects";

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        className="inline-flex h-7 items-center gap-1.5 rounded-md px-2 text-sm font-medium text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)] data-[popup-open]:bg-[var(--bg-tertiary)]"
      >
        <span className="truncate max-w-[180px]">{label}</span>
        <ChevronDown className="h-3 w-3 text-[var(--text-tertiary)]" />
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 gap-0 rounded-md p-0">
        <Command>
          <CommandInput placeholder="Find a project…" className="h-9 text-[13px]" />
          <CommandList className="p-1">
            <CommandEmpty className="py-6 text-xs">No projects.</CommandEmpty>
            {sorted.length > 0 && (
              <CommandGroup
                heading="Projects"
                className="px-0 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1 [&_[cmdk-group-heading]]:font-mono [&_[cmdk-group-heading]]:text-[10px] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-widest [&_[cmdk-group-heading]]:text-[var(--text-tertiary)]"
              >
                {sorted.map((p) => {
                  const name = p.metadata.name;
                  const active = name === currentProject;
                  return (
                    <CommandItem
                      key={name}
                      value={name}
                      onSelect={() => {
                        setOpen(false);
                        router.push(`/projects/${encodeURIComponent(name)}`);
                      }}
                      className="px-2 py-1.5"
                    >
                      <span
                        className={cn(
                          "inline-block h-1.5 w-1.5 shrink-0 rounded-full",
                          active ? "bg-[var(--accent)]" : "bg-[var(--text-tertiary)]/30"
                        )}
                      />
                      <span className="truncate text-[13px]">{name}</span>
                      {active && <Check className="ml-auto h-3 w-3 text-[var(--accent)]" />}
                    </CommandItem>
                  );
                })}
              </CommandGroup>
            )}
            <CommandSeparator />
            <CommandGroup className="px-0">
              <CommandItem
                value="__new__"
                onSelect={() => {
                  setOpen(false);
                  router.push("/projects/new");
                }}
                className="px-2 py-1.5 text-[13px] text-[var(--accent)]"
              >
                <Plus className="h-3.5 w-3.5" />
                New project
              </CommandItem>
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

function EnvironmentSwitcher({ project }: { project: string }) {
  const router = useRouter();
  const pathname = usePathname();
  const search = useSearchParams();
  const groups = useEnvGroups(project);
  const [open, setOpen] = useState(false);
  const [showNewEnv, setShowNewEnv] = useState(false);

  type EnvRow = {
    name: string;
    kind: "production" | "preview" | "custom";
    services: number;
    href: string;
  };
  const envs = useMemo<EnvRow[]>(() => {
    const list = groups.data ?? [];
    return list.map((g) => {
      const params = new URLSearchParams(search?.toString() ?? "");
      params.set("env", g.name);
      const qs = params.toString();
      const href = qs ? `${pathname}?${qs}` : pathname;
      return {
        name: g.name,
        kind:
          g.kind === "production"
            ? "production"
            : g.kind === "preview"
              ? "preview"
              : "custom",
        services: g.services?.length ?? 0,
        href,
      };
    });
  }, [groups.data, pathname, search]);

  const currentEnv = search?.get("env") ?? "production";

  // Migrated from a hand-rolled absolutely-positioned ul with manual
  // outside-click + ESC listeners to the same Popover+Command
  // primitive ProjectPicker uses. Behavioural wins: keyboard arrows,
  // type-to-filter, proper aria roles, focus management, no
  // duplicated outside-click code. The previous "roll-our-own" was
  // a workaround for a popover/cmdk pointer-event quirk that was
  // since fixed upstream; nothing in this dropdown actually needs
  // the synchronous <a href> escape hatch.
  const onPick = (href: string) => {
    setOpen(false);
    router.replace(href, { scroll: false });
  };

  return (
    <>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger
          className={cn(
            "inline-flex h-7 items-center gap-1.5 rounded-md border px-2 text-sm font-medium transition-colors",
            "border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] data-[popup-open]:bg-[var(--bg-tertiary)] data-[popup-open]:text-[var(--text-primary)]",
          )}
        >
          <span
            className={cn(
              "inline-block h-1.5 w-1.5 rounded-full",
              currentEnv === "production"
                ? "bg-emerald-400"
                : currentEnv.startsWith("pr-") || currentEnv.startsWith("preview-")
                  ? "bg-amber-400"
                  : "bg-blue-400",
            )}
          />
          <span className="truncate max-w-[160px] font-mono text-xs">{currentEnv}</span>
          <ChevronDown className="h-3 w-3 text-[var(--text-tertiary)]" />
        </PopoverTrigger>
        <PopoverContent align="start" className="w-72 gap-0 rounded-md p-0">
          <Command>
            <CommandInput placeholder="Find an env…" className="h-9 text-[13px]" />
            <CommandList className="p-1">
              <CommandEmpty className="py-6 text-xs">No environments.</CommandEmpty>
              {envs.length > 0 && (
                <CommandGroup
                  heading="Environments"
                  className="px-0 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1 [&_[cmdk-group-heading]]:font-mono [&_[cmdk-group-heading]]:text-[10px] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-widest [&_[cmdk-group-heading]]:text-[var(--text-tertiary)]"
                >
                  {envs.map((e) => {
                    const active = currentEnv === e.name;
                    return (
                      <CommandItem
                        key={e.name}
                        value={e.name}
                        onSelect={() => onPick(e.href)}
                        className="px-2 py-1.5"
                      >
                        <span
                          className={cn(
                            "inline-block h-1.5 w-1.5 shrink-0 rounded-full",
                            e.kind === "production"
                              ? "bg-emerald-400"
                              : e.kind === "preview"
                                ? "bg-amber-400"
                                : "bg-blue-400",
                          )}
                        />
                        <span className="truncate font-mono text-[12px]">{e.name}</span>
                        <span className="ml-auto shrink-0 font-mono text-[10px] text-[var(--text-tertiary)]">
                          {e.services} svc
                        </span>
                        {e.kind === "preview" && (
                          <span className="shrink-0 rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
                            PR
                          </span>
                        )}
                        {active && <Check className="ml-1 h-3 w-3 shrink-0 text-[var(--accent)]" />}
                      </CommandItem>
                    );
                  })}
                </CommandGroup>
              )}
              <CommandSeparator />
              <CommandGroup className="px-0">
                <CommandItem
                  value="__new__"
                  onSelect={() => {
                    setOpen(false);
                    setShowNewEnv(true);
                  }}
                  className="px-2 py-1.5 text-[12px] text-[var(--accent)]"
                >
                  <Plus className="h-3 w-3" />
                  New environment
                </CommandItem>
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>

      <NewEnvironmentDialog
        project={project}
        open={showNewEnv}
        onClose={() => setShowNewEnv(false)}
        onCreated={(name) => {
          const next = new URLSearchParams(search?.toString() ?? "");
          next.set("env", name);
          router.replace(`${pathname}?${next.toString()}`, { scroll: false });
        }}
      />
    </>
  );
}

// FeedEvent matches the server's NotificationEvent wire shape. Only
// the fields the popover renders are typed.
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
  // when the server's internal/failures package matched a known kind.
  // Surfaces the human one-liner as the row subtitle so the popover
  // tells the user *why* something failed instead of just "Build
  // failed". Optional — older rows and non-failure events skip it.
  classification?: {
    kind?: string;
    tab?: string;
    summary?: string;
    lineHint?: string;
    lineNum?: number;
  };
}

function NotificationsButton() {
  // Admins get the full feed with read-tracking + clear-all. Non-
  // admins get a project-scoped read-only feed (/my-feed) so they
  // still see deploy outcomes on services they own, just without
  // the read state model (which is global today).
  const isAdmin = useCan(Perms.SettingsAdmin);
  const qc = useQueryClient();
  // Controlled state so a notification's <Link> click can close the
  // popover before pushing the route — otherwise the popover stays
  // open over the new page until the user clicks elsewhere.
  const [open, setOpen] = useState(false);
  // Unread count drives the dot badge. Admin-only — the my-feed
  // path has no read-tracking, so we just always show the bell
  // without a dot for non-admins.
  const unread = useQuery<{ unread: number }>({
    queryKey: ["notifications", "unread-count"],
    queryFn: () => api("/api/notifications/feed/unread-count"),
    enabled: isAdmin,
    refetchInterval: 30_000,
    staleTime: 15_000,
    retry: false,
    throwOnError: false,
  });
  const feedPath = isAdmin
    ? "/api/notifications/feed?limit=30"
    : "/api/notifications/my-feed?limit=30";
  // Pinned to a single const so the optimistic-clear cache write and
  // the invalidation can't drift from the useQuery key. The previous
  // version had the useQuery key at ["notifications", "feed", scope]
  // but the mutation wrote/invalidated ["notifications", "feed"] —
  // React Query matches keys tuple-by-tuple, so the writes hit a
  // different cache slot and the popover only updated after a
  // close-then-reopen kicked off a fresh refetch.
  const feedKey = ["notifications", "feed", isAdmin ? "admin" : "scoped"] as const;
  const feed = useQuery<FeedEvent[]>({
    queryKey: feedKey,
    queryFn: () => api(feedPath),
    enabled: false, // only fetch on popover open
    staleTime: 15_000,
    retry: false,
    throwOnError: false,
  });
  const markRead = useMutation({
    mutationFn: () =>
      api("/api/notifications/feed/read-all", { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["notifications", "unread-count"] });
    },
  });
  // Clear-all wipes the entire feed server-side. Called from the
  // trash button in the popover header. We optimistically blank the
  // local feed cache so the empty-state appears immediately, then
  // refetch on success to confirm — and on error roll back via
  // invalidate so the previous events come back.
  const clearAll = useMutation({
    mutationFn: () => api("/api/notifications/feed", { method: "DELETE" }),
    onMutate: () => {
      qc.setQueryData(feedKey, [] as FeedEvent[]);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["notifications", "unread-count"] });
      qc.invalidateQueries({ queryKey: feedKey });
    },
    onError: () => {
      qc.invalidateQueries({ queryKey: feedKey });
    },
  });

  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) {
      void feed.refetch();
      // Mark-read only exists for admins — non-admin feed has no
      // read tracking (per-user readAt isn't modelled yet).
      if (isAdmin) markRead.mutate();
    }
  };

  const badge = (unread.data?.unread ?? 0) > 0;
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger
        aria-label="Notifications"
        className="relative inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
      >
        <Bell className="h-4 w-4" />
        {badge && (
          <span
            aria-hidden
            className="absolute right-1.5 top-1.5 inline-block h-1.5 w-1.5 rounded-full bg-[var(--accent)]"
          />
        )}
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-0">
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
          <p className="text-xs font-semibold tracking-tight">Notifications</p>
          <div className="flex items-center gap-3">
            {/* Clear-all + channels-config are admin-only. Non-
                admin viewers get a read-only feed with no mutation
                affordances — the feed is project-scoped and read
                state isn't tracked per-user yet. */}
            {isAdmin && (feed.data ?? []).length > 0 && (
              <button
                type="button"
                onClick={() => clearAll.mutate()}
                disabled={clearAll.isPending}
                title="Clear all notifications"
                aria-label="Clear all notifications"
                className="inline-flex items-center gap-1 font-mono text-[10px] text-[var(--text-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-50"
              >
                <Trash2 className="h-3 w-3" aria-hidden />
                clear
              </button>
            )}
            {isAdmin && (
              <Link
                href="/settings/notifications"
                onClick={() => setOpen(false)}
                className="font-mono text-[10px] text-[var(--accent)] hover:underline"
              >
                channels →
              </Link>
            )}
          </div>
        </header>
        <div className="max-h-96 overflow-y-auto">
          {feed.isPending ? (
            <p className="px-3 py-4 text-xs text-[var(--text-tertiary)]">Loading…</p>
          ) : feed.isError ? (
            <p className="px-3 py-4 text-xs text-red-400">
              {feed.error instanceof Error ? feed.error.message : "Failed to load"}
            </p>
          ) : (feed.data ?? []).length === 0 ? (
            <p className="px-3 py-6 text-center text-xs text-[var(--text-tertiary)]">
              No events yet. Builds, deploys, and node alerts show up here as they happen.
            </p>
          ) : (
            <ul className="divide-y divide-[var(--border-subtle)]">
              {(feed.data ?? []).map((e) => (
                <NotificationRow key={e.id} event={e} onClose={() => setOpen(false)} />
              ))}
            </ul>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}

// NotificationRow is a single bell-popover entry. When the event
// carries a URL (server populates this for build/pod/node/alert
// events — see internal/notify/notify.go), wrap the row in a Link
// that closes the popover before navigating. Otherwise render a
// plain non-interactive li so events without a meaningful target
// (e.g. low-importance generic events) don't pretend to be
// clickable.
function NotificationRow({ event, onClose }: { event: FeedEvent; onClose: () => void }) {
  const body = (
    <div className="flex items-start gap-2">
      <span
        aria-hidden
        className={cn(
          "mt-1 inline-block h-1.5 w-1.5 shrink-0 rounded-full",
          // Use the design-token severity colors so the popover stays
          // in sync with the rest of the UI when the theme switches.
          // The previous hardcoded Tailwind (red-400/amber-400/
          // emerald-400) drifted from --error/--warning/--success in
          // dark mode, where the tokens use bolder hues for contrast.
          event.severity === "error"
            ? "bg-[var(--error)]"
            : event.severity === "warn"
              ? "bg-[var(--warning)]"
              : "bg-[var(--success)]"
        )}
      />
      <div className="min-w-0 flex-1">
        <p className="truncate text-[12px] font-medium">{event.title}</p>
        {/* Prefer the classifier's human summary over the raw body
            when both are present. The summary reads "Missing env var:
            DATABASE_URL"; the raw body would say "build pod exited
            with code 1" which doesn't tell the user what to fix. */}
        {event.classification?.summary ? (
          <p className="mt-0.5 line-clamp-2 text-[11px] text-[var(--text-secondary)]">
            {event.classification.summary}
          </p>
        ) : event.body ? (
          <p className="mt-0.5 line-clamp-2 text-[11px] text-[var(--text-secondary)]">
            {event.body}
          </p>
        ) : null}
        <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
          {event.type}
          {event.project && ` · ${event.project}`}
          {event.service && `/${event.service}`}
          {" · "}
          {relativeFromNow(event.createdAt)}
        </p>
      </div>
    </div>
  );

  // Derive a navigation target. Prefer the server-supplied url
  // (newer events; carries event-specific deep links), fall back to
  // a project/service-based URL so events stored before v0.8.2 (which
  // had no url field) still navigate. Without the fallback the bell
  // popover renders a non-clickable row for every historical event,
  // which makes it look broken to a returning user.
  const href =
    event.url ||
    (event.project && event.service
      ? `/projects/${encodeURIComponent(event.project)}?service=${encodeURIComponent(event.service)}`
      : event.project
        ? `/projects/${encodeURIComponent(event.project)}`
        : "");

  if (href) {
    return (
      <li className="hover:bg-[var(--bg-tertiary)]/40 transition-colors">
        <Link
          href={href}
          onClick={onClose}
          className="block px-3 py-2"
        >
          {body}
        </Link>
      </li>
    );
  }
  return <li className="px-3 py-2">{body}</li>;
}

// relativeFromNow renders a UTC timestamp as "5m ago" / "2h ago" /
// "3d ago" without pulling in date-fns. Good enough for a feed
// where rough chronology beats precise.
function relativeFromNow(iso: string): string {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return "just now";
  const diff = Date.now() - t;
  const m = Math.floor(diff / 60_000);
  if (m < 1) return "just now";
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  return `${d}d ago`;
}

function UserMenu() {
  const { data: session } = useSession();
  const signOut = useSignOut();
  const user = session?.user;
  const initial = (user?.name?.[0] ?? user?.email?.[0] ?? "U").toUpperCase();
  const perms = session?.session.permissions ?? [];
  const canAdmin = perms.includes("user:write");

  // Popover (not DropdownMenu) — base-ui's Menu primitive was the only
  // thing in the app using that API surface; it had a hydration/portal
  // edge case that rendered the Next.js "This page couldn't load"
  // splash on first open. ServersPopover uses Popover successfully on
  // every page so we mirror that pattern here. Each row is a plain
  // <Link> or <button>, no special focus-trap logic — the popover
  // closes via outside-click.
  return (
    <Popover>
      <PopoverTrigger
        aria-label="Account menu"
        className="inline-flex h-8 items-center justify-center rounded-full focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--accent)]"
      >
        <Avatar className="h-7 w-7 border border-[var(--border-subtle)]">
          {user?.image && <AvatarImage src={user.image} alt={user.name ?? ""} />}
          <AvatarFallback className="bg-[var(--bg-tertiary)] text-[10px] font-medium">
            {initial}
          </AvatarFallback>
        </Avatar>
      </PopoverTrigger>
      {/* Trimmed menu. Used to be a 10-row flat index of every
          settings page; now it's just "your account" + a single
          "Settings…" escape that lands on /settings (which has
          its own search). The previous shape made the menu
          taller than the viewport on a 720p browser and forced
          users to scan a long list for an action that already
          exists on /settings — every click in here was wasted
          chrome. The remaining rows are the personal-scope
          actions (profile, tokens) and admin shortcuts when the
          user has them. Cluster-wide knobs (nodes, backups,
          updates, instance secrets, users, groups) all live on
          /settings now. */}
      <PopoverContent align="end" className="w-56 p-1">
        <div className="border-b border-[var(--border-subtle)] px-2 py-1.5">
          <p className="truncate text-sm font-medium">{user?.name ?? "User"}</p>
          <p className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">
            {user?.email ?? ""}
          </p>
        </div>
        <MenuRow href="/settings/profile" icon={UserIcon}>Profile</MenuRow>
        <MenuRow href="/settings/tokens" icon={KeyRound}>API tokens</MenuRow>
        <div className="my-1 h-px bg-[var(--border-subtle)]" />
        <MenuRow href="/settings" icon={Settings}>Settings…</MenuRow>
        {canAdmin && <MenuRow href="/settings/users" icon={Users}>Users &amp; groups</MenuRow>}
        <div className="my-1 h-px bg-[var(--border-subtle)]" />
        <button
          type="button"
          onClick={() => signOut()}
          className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
        >
          <LogOut className="h-3.5 w-3.5" />
          Sign out
        </button>
      </PopoverContent>
    </Popover>
  );
}

// MenuRow is a Link with the same hover affordance as a DropdownMenuItem,
// but it doesn't depend on base-ui's Menu primitive.
function MenuRow({
  href,
  icon: Icon,
  children,
}: {
  href: string;
  icon: React.ComponentType<{ className?: string }>;
  children: React.ReactNode;
}) {
  return (
    <Link
      href={href}
      className="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
    >
      <Icon className="h-3.5 w-3.5" />
      {children}
    </Link>
  );
}

