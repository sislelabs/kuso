"use client";

import Link from "next/link";
import { useRouter, usePathname, useSearchParams } from "next/navigation";
import { Fragment } from "react";
import { useEffect, useMemo, useRef, useState } from "react";
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
  Server,
  Users,
  UsersRound,
  HardDrive,
  Package,
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
          icon either way; ThemeToggle is icon-only. */}
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
  const wrapRef = useRef<HTMLDivElement>(null);

  type EnvRow = {
    name: string;
    kind: "production" | "preview" | "custom";
    services: number;
    href: string;
  };
  const envs = useMemo<EnvRow[]>(() => {
    const list = groups.data ?? [];
    return list.map((g) => {
      // Always set ?env=<name>, even for production. The previous
      // approach (delete env when "production") produced a bare
      // pathname href; some user-side state (browser cache, sw,
      // intermediate React effect) wasn't reacting to the implicit
      // production transition. With an explicit ?env=production
      // every click changes the URL string, so any reactive code
      // that watches search params is guaranteed to fire.
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

  // Roll-our-own dropdown. Each row is a real <a href> so the
  // browser navigates synchronously on click — bypasses every
  // popover/cmdk pointer-event quirk we hit before. We still call
  // router.replace from the onClick to keep client-side state in
  // sync (no full reload), but the href is the load-bearing
  // contract: even if onClick is ever swallowed, the URL still
  // changes.
  useEffect(() => {
    if (!open) return;
    const onDoc = (ev: MouseEvent) => {
      const el = wrapRef.current;
      if (el && !el.contains(ev.target as Node)) setOpen(false);
    };
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDoc);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  return (
    <div ref={wrapRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className={cn(
          "inline-flex h-7 items-center gap-1.5 rounded-md border px-2 text-sm font-medium transition-colors",
          "border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]",
          open && "bg-[var(--bg-tertiary)] text-[var(--text-primary)]",
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
        <ChevronDown className={cn("h-3 w-3 transition-transform", open && "rotate-180")} />
      </button>

      {open && (
        <div
          className="absolute left-0 top-full z-50 mt-1 w-72 overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] shadow-[var(--shadow-lg)]"
          role="menu"
        >
          <div className="border-b border-[var(--border-subtle)] px-3 py-2">
            <p className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              Environments
            </p>
            <p className="mt-0.5 text-[11px] text-[var(--text-secondary)]">
              Pick an env to view its services on the canvas.
            </p>
          </div>
          <ul className="p-1">
            {envs.length === 0 && (
              <li className="px-2 py-2 text-[12px] text-[var(--text-tertiary)]">
                No environments yet.
              </li>
            )}
            {envs.map((e) => {
              const active = currentEnv === e.name;
              return (
                <li key={e.name}>
                  <a
                    href={e.href}
                    onClick={(ev) => {
                      // Modifier-clicks (cmd/ctrl/middle) keep their
                      // native open-in-new-tab behavior; the SPA
                      // intercept only fires for plain left-clicks.
                      if (
                        ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey ||
                        ev.button !== 0
                      ) {
                        return;
                      }
                      ev.preventDefault();
                      router.replace(e.href, { scroll: false });
                      setOpen(false);
                    }}
                    className={cn(
                      "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left transition-colors no-underline",
                      active
                        ? "bg-[var(--accent-subtle)] text-[var(--accent)]"
                        : "text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)]",
                    )}
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
                    {active && <Check className="h-3 w-3 shrink-0 text-[var(--accent)]" />}
                  </a>
                </li>
              );
            })}
          </ul>
          <div className="border-t border-[var(--border-subtle)] p-1">
            <button
              type="button"
              onClick={() => {
                setOpen(false);
                setShowNewEnv(true);
              }}
              title="Mirror every service + addon under a new env name. Each cloned service gets its own URL."
              className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-[12px] text-[var(--accent)] transition-colors hover:bg-[var(--accent-subtle)]"
            >
              <Plus className="h-3 w-3" />
              New environment
            </button>
          </div>
        </div>
      )}

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
    </div>
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
}

function NotificationsButton() {
  // The feed is admin-only on the server. Hide the bell entirely
  // for non-admins so they don't see a control that always reads
  // empty + 401s the popover. Hooks run unconditionally above the
  // gate so a logout / demote (canSee flipping true→false) doesn't
  // change hook count between renders.
  const canSee = useCan(Perms.SettingsAdmin);
  const qc = useQueryClient();
  // Controlled state so a notification's <Link> click can close the
  // popover before pushing the route — otherwise the popover stays
  // open over the new page until the user clicks elsewhere.
  const [open, setOpen] = useState(false);
  // Unread count drives the dot badge. Polled every 30s — same
  // cadence the project-status query uses, so we don't add a
  // chatter to the server for one icon. enabled: canSee saves the
  // wasted poll for non-admins (they'd 401 anyway).
  const unread = useQuery<{ unread: number }>({
    queryKey: ["notifications", "unread-count"],
    queryFn: () => api("/api/notifications/feed/unread-count"),
    enabled: canSee,
    refetchInterval: 30_000,
    staleTime: 15_000,
    retry: false,
    throwOnError: false,
  });
  const feed = useQuery<FeedEvent[]>({
    queryKey: ["notifications", "feed"],
    queryFn: () => api("/api/notifications/feed?limit=30"),
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
      qc.setQueryData(["notifications", "feed"], [] as FeedEvent[]);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["notifications", "unread-count"] });
      qc.invalidateQueries({ queryKey: ["notifications", "feed"] });
    },
    onError: () => {
      qc.invalidateQueries({ queryKey: ["notifications", "feed"] });
    },
  });

  // Gate goes here, AFTER every hook, so a canSee transition doesn't
  // change the hook count between renders.
  if (!canSee) return null;

  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) {
      void feed.refetch();
      // Mark-read fires on open so the dot disappears the moment
      // the popover shows. Optimistic; the server-side count
      // refetches via invalidate in the mutation onSuccess.
      markRead.mutate();
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
            {/* Clear-all: wipes every event from the in-app feed.
                Hidden when the feed is already empty so the button
                doesn't beg to be clicked on a fresh install. Disabled
                while the mutation is in flight. */}
            {(feed.data ?? []).length > 0 && (
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
            <Link
              href="/settings/notifications"
              onClick={() => setOpen(false)}
              className="font-mono text-[10px] text-[var(--accent)] hover:underline"
            >
              channels →
            </Link>
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
          event.severity === "error"
            ? "bg-red-400"
            : event.severity === "warn"
              ? "bg-amber-400"
              : "bg-emerald-400"
        )}
      />
      <div className="min-w-0 flex-1">
        <p className="truncate text-[12px] font-medium">{event.title}</p>
        {event.body && (
          <p className="mt-0.5 line-clamp-2 text-[11px] text-[var(--text-secondary)]">
            {event.body}
          </p>
        )}
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
  const canConfig = perms.includes("config:read");

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
      <PopoverContent align="end" className="w-60 p-1">
        <div className="border-b border-[var(--border-subtle)] px-2 py-1.5">
          <p className="truncate text-sm font-medium">{user?.name ?? "User"}</p>
          <p className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">
            {user?.email ?? ""}
          </p>
        </div>
        <MenuRow href="/settings/profile" icon={UserIcon}>Profile</MenuRow>
        <MenuRow href="/settings/tokens" icon={KeyRound}>API tokens</MenuRow>
        <MenuRow href="/settings/notifications" icon={Bell}>Notifications</MenuRow>
        <MenuRow href="/settings/alerts" icon={Bell}>Alert rules</MenuRow>
        {canAdmin && <MenuRow href="/settings/instance-secrets" icon={KeyRound}>Instance secrets</MenuRow>}
        <div className="my-1 h-px bg-[var(--border-subtle)]" />
        <MenuRow href="/settings/nodes" icon={Server}>Cluster nodes</MenuRow>
        {canConfig && <MenuRow href="/settings/config" icon={Settings}>Cluster config</MenuRow>}
        <MenuRow href="/settings/backups" icon={HardDrive}>Backups</MenuRow>
        <UpdatesMenuRow />
        {canAdmin && (
          <>
            <div className="my-1 h-px bg-[var(--border-subtle)]" />
            <p className="px-2 py-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              Admin
            </p>
            <MenuRow href="/settings/users" icon={Users}>Users</MenuRow>
            <MenuRow href="/settings/groups" icon={UsersRound}>Groups</MenuRow>
          </>
        )}
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

// UpdatesMenuItem renders the menu row + a dot when an update is
// available. Polled with a generous staleTime — the server pulls
// from GitHub every 6h, no point hitting it from every menu open.
function UpdatesMenuRow() {
  const v = useQuery<{ needsUpdate?: boolean; latest?: string }>({
    queryKey: ["system", "version"],
    queryFn: () => api("/api/system/version"),
    staleTime: 5 * 60_000,
    refetchInterval: 5 * 60_000,
    retry: false,
    throwOnError: false,
  });
  const needs = !!v.data?.needsUpdate;
  return (
    <Link
      href="/settings/updates"
      className="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
    >
      <Package className="h-3.5 w-3.5" />
      Updates
      {needs && (
        <span
          className="ml-auto inline-flex items-center gap-1 rounded-full bg-[var(--accent-subtle)] px-1.5 py-0.5 font-mono text-[9px] text-[var(--accent)]"
          title={`Update available: ${v.data?.latest ?? ""}`}
        >
          <span className="h-1 w-1 rounded-full bg-[var(--accent)]" />
          new
        </span>
      )}
    </Link>
  );
}
