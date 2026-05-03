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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
  Shield,
  UsersRound,
  HardDrive,
  Package,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
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
        href="/settings/profile"
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
    roles: "Roles",
    groups: "Groups",
  };
  const out: { label: string; href?: string }[] = [
    { label: "Settings", href: "/settings/profile" },
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

function NotificationsButton() {
  // Placeholder for the upcoming notification feed. Renders a quiet
  // bell with no unread badge until the API lands.
  return (
    <button
      type="button"
      aria-label="Notifications"
      className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
    >
      <Bell className="h-4 w-4" />
    </button>
  );
}

function UserMenu() {
  const { data: session } = useSession();
  const signOut = useSignOut();
  const user = session?.user;
  const initial = (user?.name?.[0] ?? user?.email?.[0] ?? "U").toUpperCase();
  const perms = session?.session.permissions ?? [];
  const canAdmin = perms.includes("user:write");
  const canConfig = perms.includes("config:read");

  return (
    <DropdownMenu>
      <DropdownMenuTrigger className="inline-flex h-8 items-center justify-center rounded-full">
        <Avatar className="h-7 w-7 border border-[var(--border-subtle)]">
          {user?.image && <AvatarImage src={user.image} alt={user.name ?? ""} />}
          <AvatarFallback className="bg-[var(--bg-tertiary)] text-[10px] font-medium">
            {initial}
          </AvatarFallback>
        </Avatar>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-60">
        <DropdownMenuLabel className="flex flex-col gap-0.5">
          <span className="truncate text-sm font-medium">{user?.name ?? "User"}</span>
          <span className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">
            {user?.email ?? ""}
          </span>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          render={
            <Link href="/settings/profile" className="flex items-center gap-2">
              <UserIcon className="h-3.5 w-3.5" />
              Profile
            </Link>
          }
        />
        <DropdownMenuItem
          render={
            <Link href="/settings/tokens" className="flex items-center gap-2">
              <KeyRound className="h-3.5 w-3.5" />
              API tokens
            </Link>
          }
        />
        <DropdownMenuItem
          render={
            <Link href="/settings/notifications" className="flex items-center gap-2">
              <Bell className="h-3.5 w-3.5" />
              Notifications
            </Link>
          }
        />
        <DropdownMenuSeparator />
        <DropdownMenuItem
          render={
            <Link href="/settings/nodes" className="flex items-center gap-2">
              <Server className="h-3.5 w-3.5" />
              Cluster nodes
            </Link>
          }
        />
        {canConfig && (
          <DropdownMenuItem
            render={
              <Link href="/settings/config" className="flex items-center gap-2">
                <Settings className="h-3.5 w-3.5" />
                Cluster config
              </Link>
            }
          />
        )}
        <DropdownMenuItem
          render={
            <Link href="/settings/backups" className="flex items-center gap-2">
              <HardDrive className="h-3.5 w-3.5" />
              Backups
            </Link>
          }
        />
        <UpdatesMenuItem />
        {canAdmin && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              Admin
            </DropdownMenuLabel>
            <DropdownMenuItem
              render={
                <Link href="/settings/users" className="flex items-center gap-2">
                  <Users className="h-3.5 w-3.5" />
                  Users
                </Link>
              }
            />
            <DropdownMenuItem
              render={
                <Link href="/settings/roles" className="flex items-center gap-2">
                  <Shield className="h-3.5 w-3.5" />
                  Roles
                </Link>
              }
            />
            <DropdownMenuItem
              render={
                <Link href="/settings/groups" className="flex items-center gap-2">
                  <UsersRound className="h-3.5 w-3.5" />
                  Groups
                </Link>
              }
            />
          </>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onClick={() => signOut()}
          className="flex items-center gap-2 text-[var(--text-secondary)]"
        >
          <LogOut className="h-3.5 w-3.5" />
          Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// UpdatesMenuItem renders the menu row + a dot when an update is
// available. Polled with a generous staleTime — the server pulls
// from GitHub every 6h, no point hitting it from every menu open.
function UpdatesMenuItem() {
  const v = useQuery<{ needsUpdate?: boolean; latest?: string }>({
    queryKey: ["system", "version"],
    queryFn: () => api("/api/system/version"),
    staleTime: 5 * 60_000,
    refetchInterval: 5 * 60_000,
  });
  const needs = !!v.data?.needsUpdate;
  return (
    <DropdownMenuItem
      render={
        <Link href="/settings/updates" className="flex items-center gap-2">
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
      }
    />
  );
}
