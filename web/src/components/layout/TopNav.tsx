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
import { useSession, useSignOut } from "@/features/auth";
import { useProjects, useProject } from "@/features/projects";
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
      className="sticky top-0 z-40 flex h-12 shrink-0 items-center gap-1.5 border-b border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 lg:px-4"
    >
      <Link href="/projects" aria-label="Home" className="mr-1.5 flex shrink-0 items-center">
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

      {currentProject && !settingsCrumbs && (
        <Crumb>
          <EnvironmentSwitcher project={currentProject} />
        </Crumb>
      )}

      {settingsCrumbs?.map((c, i) => (
        <Fragment key={c.href ?? c.label}>
          <Crumb>
            {c.href ? (
              <Link
                href={c.href}
                className="inline-flex h-7 items-center gap-1.5 rounded-md px-2 text-sm font-medium text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                {c.label}
              </Link>
            ) : (
              <span className="inline-flex h-7 items-center px-2 text-sm font-medium text-[var(--text-primary)]">
                {c.label}
              </span>
            )}
          </Crumb>
          {void i}
        </Fragment>
      ))}

      <div className="flex-1" />

      <Link
        href="/settings"
        className="inline-flex h-7 items-center gap-1.5 rounded-md px-2 text-xs font-medium text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
      >
        <Settings className="h-3.5 w-3.5" />
        Settings
      </Link>
      <ServersPopover />
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
function Crumb({ children }: { children: React.ReactNode }) {
  return (
    <>
      <ChevronRight className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]/60" />
      {children}
    </>
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
  const data = useProject(project);
  const [open, setOpen] = useState(false);
  const [showNewEnv, setShowNewEnv] = useState(false);

  // Unique env names, production first, previews after sorted by PR
  // number desc.
  const envs = useMemo(() => {
    const list = data.data?.environments ?? [];
    const prod = list.filter((e) => e.spec.kind === "production");
    const previews = list
      .filter((e) => e.spec.kind === "preview")
      .sort((a, b) => (b.spec.pullRequest?.number ?? 0) - (a.spec.pullRequest?.number ?? 0));
    return [...prod, ...previews];
  }, [data.data]);

  const currentEnv = search?.get("env") ?? "production";
  const labelFor = (envName: string) => envName.replace(/^.*?-/, "");

  const setEnv = (name: string) => {
    const next = new URLSearchParams(search?.toString() ?? "");
    if (name === "production") next.delete("env");
    else next.set("env", name);
    const qs = next.toString();
    router.replace(qs ? `${pathname}?${qs}` : pathname, { scroll: false });
    setOpen(false);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger className="inline-flex h-7 items-center gap-1.5 rounded-md px-2 text-sm font-medium text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] data-[popup-open]:bg-[var(--bg-tertiary)]">
        <span className="truncate max-w-[160px] font-mono text-xs">{currentEnv}</span>
        <ChevronDown className="h-3 w-3" />
      </PopoverTrigger>
      <PopoverContent align="start" className="w-64 gap-0 rounded-md p-0">
        <Command>
          <CommandInput placeholder="Switch environment…" className="h-9 text-[13px]" />
          <CommandList className="p-1">
            <CommandEmpty className="py-6 text-xs">No environments yet.</CommandEmpty>
            {envs.length > 0 && (
              <CommandGroup
                heading="Environments"
                className="px-0 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1 [&_[cmdk-group-heading]]:font-mono [&_[cmdk-group-heading]]:text-[10px] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-widest [&_[cmdk-group-heading]]:text-[var(--text-tertiary)]"
              >
                {envs.map((e) => {
                  const short = labelFor(e.metadata.name);
                  const active = (currentEnv === "production" && e.spec.kind === "production") || currentEnv === short;
                  return (
                    <CommandItem
                      key={e.metadata.uid ?? e.metadata.name}
                      value={short}
                      onSelect={() => setEnv(e.spec.kind === "production" ? "production" : short)}
                      className="px-2 py-1.5"
                    >
                      <span
                        className={cn(
                          "inline-block h-1.5 w-1.5 shrink-0 rounded-full",
                          e.spec.kind === "production"
                            ? "bg-emerald-400"
                            : "bg-amber-400"
                        )}
                      />
                      <span className="truncate font-mono text-[12px]">{short}</span>
                      {e.spec.kind === "preview" && (
                        <span className="ml-auto rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
                          preview
                        </span>
                      )}
                      {active && <Check className={cn("h-3 w-3 text-[var(--accent)]", e.spec.kind === "production" ? "ml-auto" : "ml-1")} />}
                    </CommandItem>
                  );
                })}
              </CommandGroup>
            )}
            {/* Trailing "new env" row. Sits below the env list so the
                user always sees their existing envs first; click ->
                opens the modal, popover closes itself. */}
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

      <NewEnvironmentDialog
        project={project}
        open={showNewEnv}
        onClose={() => setShowNewEnv(false)}
        onCreated={(name) => {
          // Switch into the freshly-created env so the user lands
          // on its canvas (envs differ by ?env= search param).
          const next = new URLSearchParams(search?.toString() ?? "");
          next.set("env", name);
          router.replace(`${pathname}?${next.toString()}`, { scroll: false });
        }}
      />
    </Popover>
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
  const qc = useQueryClient();
  // Unread count drives the dot badge. Polled every 30s — same
  // cadence the project-status query uses, so we don't add a
  // chatter to the server for one icon.
  const unread = useQuery<{ unread: number }>({
    queryKey: ["notifications", "unread-count"],
    queryFn: () => api("/api/notifications/feed/unread-count"),
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

  const onOpenChange = (next: boolean) => {
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
    <Popover onOpenChange={onOpenChange}>
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
          <Link
            href="/settings/notifications"
            className="font-mono text-[10px] text-[var(--accent)] hover:underline"
          >
            channels →
          </Link>
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
                <li key={e.id} className="px-3 py-2">
                  <div className="flex items-start gap-2">
                    <span
                      aria-hidden
                      className={cn(
                        "mt-1 inline-block h-1.5 w-1.5 shrink-0 rounded-full",
                        e.severity === "error"
                          ? "bg-red-400"
                          : e.severity === "warn"
                            ? "bg-amber-400"
                            : "bg-emerald-400"
                      )}
                    />
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-[12px] font-medium">{e.title}</p>
                      {e.body && (
                        <p className="mt-0.5 line-clamp-2 text-[11px] text-[var(--text-secondary)]">
                          {e.body}
                        </p>
                      )}
                      <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
                        {e.type}
                        {e.project && ` · ${e.project}`}
                        {e.service && `/${e.service}`}
                        {" · "}
                        {relativeFromNow(e.createdAt)}
                      </p>
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
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
