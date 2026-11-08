"use client";

import Link from "next/link";
import { useRouter, usePathname, useSearchParams } from "next/navigation";
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
import { useSession, useSignOut, useCan, useCanOnProject, Perms } from "@/features/auth";
import {
  useProjects,
  useEnvGroups,
  useProject,
  deleteEnvGroup,
  envGroupsQueryKey,
} from "@/features/projects";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { envGroupLabel } from "@/lib/env-group";
import { pullRequestUrl } from "@/lib/pull-request-url";
import { toast } from "sonner";
import { useRouteParams } from "@/lib/dynamic-params";
import { cn } from "@/lib/utils";
import {
  Check,
  ChevronDown,
  LogOut,
  Plus,
  User as UserIcon,
  KeyRound,
  Settings,
  Users,
  Trash2,
  GitPullRequest,
  Keyboard,
  Monitor,
  Moon,
  Sun,
} from "lucide-react";
import { useTheme } from "next-themes";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Kbd } from "@/components/ui/kbd";
import { openShortcutsHelp } from "@/lib/shortcuts";
import { classifyError } from "@/lib/error-kind";
import { NotificationsButton } from "./Notifications";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { ServersPopover } from "./ServersPopover";
import { CommandTrigger } from "@/components/command/CommandPalette";
import { NewEnvironmentDialog } from "./NewEnvironmentDialog";

// TopNav is the persistent shell across every authenticated page. Left:
// logo, then a breadcrumb (project picker → environment, or Settings →
// section). Right: search, shortcuts, settings, nodes, theme,
// notifications, account. Labels collapse to icons below lg so the bar
// never wraps; below sm the global actions move to <MobileNav>.
export function TopNav() {
  const params = useRouteParams<{ project: string }>(["project"]);
  const currentProject = params.project ?? "";
  const pathname = usePathname() ?? "";
  const inSettings = pathname === "/settings" || pathname.startsWith("/settings/");
  const settingsCrumbs = inSettings ? settingsBreadcrumb(pathname) : null;
  // A project URL that 404s gets no env switcher or settings cog — they'd
  // offer actions on something that isn't there.
  const projectQuery = useProject(currentProject);
  const projectMissing = projectQuery.isError && classifyError(projectQuery.error) === "not-found";
  const projectScoped = !!currentProject && !settingsCrumbs && !projectMissing;

  return (
    <header className="sticky top-0 z-40 flex h-12 shrink-0 items-center gap-1 border-b border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 sm:px-3 lg:px-4">
      <nav aria-label="Location" className="flex min-w-0 flex-1 items-center gap-1">
        <Link
          href="/projects"
          aria-label="kuso — all projects"
          className="mr-0.5 flex shrink-0 items-center rounded-md p-1 sm:mr-1"
        >
          <Logo showText={false} className="sm:hidden" />
          <Logo className="hidden sm:inline-flex" />
        </Link>

        {/* No project picker on /settings/* — it would claim a project
            is in scope when none is. */}
        {!settingsCrumbs && (
          <Crumb className="min-w-0">
            <ProjectPicker currentProject={currentProject} />
          </Crumb>
        )}

        {projectScoped && (
          <Crumb className="hidden sm:flex">
            <EnvironmentSwitcher project={currentProject} />
          </Crumb>
        )}

        {/* "Settings for the thing in scope" — the only visible path to
            project-level destructive actions and preview TTLs. */}
        {projectScoped && (
          <NavIconLink
            href={`/projects/${encodeURIComponent(currentProject)}/settings`}
            label="Project settings"
            className="hidden sm:inline-flex"
          >
            <Settings />
          </NavIconLink>
        )}

        {settingsCrumbs?.map((c, i) => (
          <Crumb key={c.href ?? c.label} className={i === 0 && settingsCrumbs.length > 1 ? "hidden sm:flex" : "min-w-0"}>
            {c.href ? (
              <Link
                href={c.href}
                className="inline-flex h-7 items-center rounded-md px-2 text-sm font-medium text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                {c.label}
              </Link>
            ) : (
              <span aria-current="page" className="inline-flex h-7 min-w-0 items-center px-2 text-sm font-medium text-[var(--text-primary)]">
                <span className="truncate">{c.label}</span>
              </span>
            )}
          </Crumb>
        ))}
      </nav>

      <div className="flex shrink-0 items-center gap-0.5 sm:gap-1">
        {/* Search + global actions live in the bottom bar on phones. */}
        <div className="hidden sm:flex">
          <CommandTrigger />
        </div>
        <NavIconButton
          label="Keyboard shortcuts"
          shortcut="?"
          onClick={openShortcutsHelp}
          className="hidden md:inline-flex"
        >
          <Keyboard />
        </NavIconButton>
        <Link
          href="/settings"
          aria-label="Settings"
          title="Settings"
          className="relative hidden h-8 items-center gap-1.5 rounded-md px-2 text-[13px] font-medium whitespace-nowrap text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] sm:inline-flex"
        >
          <Settings className="size-4" aria-hidden />
          <span className="hidden xl:inline">Settings</span>
          <UpdateDot />
        </Link>
        <div className="hidden sm:flex">
          <ServersPopover />
        </div>
        <div className="hidden sm:flex">
          <ThemeToggle />
        </div>
        <NotificationsButton className="hidden sm:inline-flex" />
        <UserMenu />
      </div>
    </header>
  );
}

const navIcon =
  "inline-flex size-8 shrink-0 items-center justify-center rounded-md text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] [&_svg]:size-4";

function NavIconLink({
  href,
  label,
  className,
  children,
}: {
  href: string;
  label: string;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={<Link href={href} aria-label={label} className={cn(navIcon, "size-7 text-[var(--text-tertiary)] [&_svg]:size-3.5", className)} />}
      >
        {children}
      </TooltipTrigger>
      <TooltipContent side="bottom">{label}</TooltipContent>
    </Tooltip>
  );
}

function NavIconButton({
  label,
  shortcut,
  onClick,
  className,
  children,
}: {
  label: string;
  shortcut?: string;
  onClick: () => void;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={<button type="button" aria-label={label} onClick={onClick} className={cn(navIcon, className)} />}
      >
        {children}
      </TooltipTrigger>
      <TooltipContent side="bottom">
        {label}
        {shortcut && <Kbd className="border-transparent bg-[var(--bg-primary)]/20 text-inherit">{shortcut}</Kbd>}
      </TooltipContent>
    </Tooltip>
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
    activity: "Activity",
    alerts: "Alerts",
    backups: "Backups",
    builds: "Builds",
    database: "Database",
    drains: "Log drains",
    github: "GitHub",
    health: "Health",
    import: "Import",
    "import-compose": "Import compose",
    "incident-agent": "Incident agent",
    "instance-secrets": "Instance secrets",
    roles: "Roles",
    sessions: "Sessions",
    updates: "Updates",
    usage: "Usage",
  };
  if (!section) return [{ label: "Settings" }];
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
    <span className={cn("flex shrink-0 items-center gap-0.5", className)}>
      <span aria-hidden className="shrink-0 text-[var(--border-strong)] select-none">/</span>
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
        aria-label={currentProject ? `Project: ${currentProject}. Switch project` : "Switch project"}
        className="inline-flex h-7 min-w-0 items-center gap-1.5 rounded-md px-2 text-sm font-medium text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)] data-[popup-open]:bg-[var(--bg-tertiary)]"
      >
        <span className="truncate max-w-[40vw] sm:max-w-[200px]">{label}</span>
        <ChevronDown className="size-3.5 shrink-0 text-[var(--text-tertiary)]" aria-hidden />
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 gap-0 rounded-md p-0">
        <Command>
          <CommandInput placeholder="Find a project…" className="h-9 text-[13px]" />
          <CommandList className="p-1">
            <CommandEmpty className="py-6 text-sm">No matching projects.</CommandEmpty>
            {sorted.length > 0 && (
              <CommandGroup
                heading="Projects"
                className="px-0 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-[var(--text-tertiary)]"
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
                          active ? "bg-[var(--accent)]" : "bg-[var(--border-default)]"
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

  // PR context for a preview env: number, branch and a GitHub link, read
  // off the env CRs the project describe already returns.
  const projectData = useProject(project);
  const preview = useMemo(() => {
    const d = projectData.data;
    if (!d || currentEnv === "production") return null;
    const env = d.environments.find(
      (e) => envGroupLabel(e) === currentEnv && e.spec.pullRequest?.number,
    );
    if (!env) return null;
    const pr = env.spec.pullRequest?.number;
    const branch = env.spec.pullRequest?.headRef || env.spec.branch;
    const svc = d.services.find((s) => s.metadata.name === env.spec.service);
    const repo = svc?.spec.repo?.url ?? d.project.spec.defaultRepo?.url;
    return { pr, branch, url: pullRequestUrl(repo, pr), expiresAt: env.spec.ttl?.expiresAt };
  }, [projectData.data, currentEnv]);

  const qc = useQueryClient();
  const canDelete = useCanOnProject(project, Perms.ServicesWrite);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);

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
            "border-[var(--border-default)] bg-[var(--bg-secondary)] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] data-[popup-open]:bg-[var(--bg-tertiary)] data-[popup-open]:text-[var(--text-primary)]",
          )}
        >
          <span
            aria-hidden
            className={cn(
              "inline-block h-1.5 w-1.5 rounded-full",
              currentEnv === "production"
                ? "bg-[var(--success)]"
                : currentEnv.startsWith("pr-") || currentEnv.startsWith("preview-")
                  ? "bg-[var(--info)]"
                  : "bg-[var(--text-tertiary)]",
            )}
          />
          <span className="sr-only">
            {currentEnv === "production"
              ? "Production environment:"
              : currentEnv.startsWith("pr-") || currentEnv.startsWith("preview-")
                ? "Preview environment:"
                : "Environment:"}
          </span>
          <span className="truncate max-w-[160px] text-[13px]">{currentEnv}</span>
          <ChevronDown className="size-3.5 text-[var(--text-tertiary)]" aria-hidden />
        </PopoverTrigger>
        <PopoverContent align="start" className="w-72 gap-0 rounded-md p-0">
          <Command>
            <CommandInput placeholder="Find an env…" className="h-9 text-[13px]" />
            <CommandList className="p-1">
              <CommandEmpty className="py-6 text-sm">No matching environments.</CommandEmpty>
              {envs.length > 0 && (
                <CommandGroup
                  heading="Environments"
                  className="px-0 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-[var(--text-tertiary)]"
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
                              ? "bg-[var(--success)]"
                              : e.kind === "preview"
                                ? "bg-[var(--info)]"
                                : "bg-[var(--text-tertiary)]",
                          )}
                        />
                        <span className="truncate text-[13px]">{e.name}</span>
                        <span className="ml-auto shrink-0 text-xs tabular-nums text-[var(--text-tertiary)]">
                          {e.services} {e.services === 1 ? "service" : "services"}
                        </span>
                        {e.kind === "preview" && (
                          <span className="shrink-0 rounded bg-[var(--info-subtle)] px-1.5 py-0.5 text-2xs font-medium text-[var(--info)]">
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
                {currentEnv !== "production" && canDelete && (
                  <CommandItem
                    value="__delete__"
                    onSelect={() => {
                      setOpen(false);
                      setConfirmDelete(true);
                    }}
                    className="px-2 py-1.5 text-[13px] text-[var(--danger)]"
                  >
                    <Trash2 className="size-3.5" aria-hidden />
                    Delete {currentEnv}…
                  </CommandItem>
                )}
                <CommandItem
                  value="__new__"
                  onSelect={() => {
                    setOpen(false);
                    setShowNewEnv(true);
                  }}
                  className="px-2 py-1.5 text-[13px] text-[var(--link)]"
                >
                  <Plus className="size-3.5" aria-hidden />
                  New environment
                </CommandItem>
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>

      {preview && (
        <span
          className="hidden items-center gap-1 text-xs text-[var(--text-tertiary)] lg:inline-flex"
          title={
            preview.expiresAt
              ? `Preview expires ${new Date(preview.expiresAt).toLocaleString()}`
              : undefined
          }
        >
          <GitPullRequest className="h-3 w-3" aria-hidden />
          {preview.url ? (
            <a
              href={preview.url}
              target="_blank"
              rel="noreferrer"
              className="text-[var(--accent)] hover:underline"
            >
              #{preview.pr}
            </a>
          ) : (
            <span>#{preview.pr}</span>
          )}
          {preview.branch && <span className="max-w-[160px] truncate font-mono">{preview.branch}</span>}
        </span>
      )}

      <ConfirmDialog
        open={confirmDelete}
        title={`Delete environment ${currentEnv}`}
        body={
          <>
            Deletes every service, environment and env-scoped addon in{" "}
            <span className="font-mono text-[var(--text-primary)]">{currentEnv}</span>, including
            the data in its own databases. Production is not touched.
            {preview && " A new push to the PR can create the preview again."}
          </>
        }
        typeToConfirm={currentEnv}
        confirmLabel="Delete environment"
        pending={deleting}
        onCancel={() => setConfirmDelete(false)}
        onConfirm={async () => {
          const name = currentEnv;
          setDeleting(true);
          try {
            await deleteEnvGroup(project, name);
            toast.success(`Environment ${name} deleted`);
            setConfirmDelete(false);
            qc.invalidateQueries({ queryKey: envGroupsQueryKey(project) });
            qc.invalidateQueries({ queryKey: ["projects", project] });
            const next = new URLSearchParams(search?.toString() ?? "");
            next.delete("env");
            const qs = next.toString();
            router.replace(qs ? `${pathname}?${qs}` : (pathname ?? "/"), { scroll: false });
          } catch (err) {
            toast.error(err instanceof Error ? err.message : "Failed to delete environment");
          } finally {
            setDeleting(false);
          }
        }}
      />

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

// UpdateDot marks the Settings link when a newer kuso release exists.
// Shares the updates page's query key so both stay in sync.
function UpdateDot() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const version = useQuery<{ needsUpdate: boolean; latest: string }>({
    queryKey: ["system", "version"],
    queryFn: () => api("/api/system/version"),
    enabled: isAdmin,
    staleTime: 5 * 60_000,
  });
  if (!isAdmin || !version.data?.needsUpdate) return null;
  return (
    <span
      className="absolute right-1 top-1 size-2 rounded-full bg-[var(--warning)] ring-2 ring-[var(--bg-primary)]"
      title={`Update available: ${version.data.latest}`}
      aria-label="Update available"
    />
  );
}

function UserMenu() {
  const { data: session } = useSession();
  const signOut = useSignOut();
  const user = session?.user;
  const initial = (user?.name?.[0] ?? user?.email?.[0] ?? "U").toUpperCase();
  const perms = session?.session.permissions ?? [];
  const canAdmin = perms.includes("user:write");
  // Controlled so a row click closes it: client-side navigation keeps
  // TopNav mounted, so an uncontrolled popover stayed open on the new page.
  const [open, setOpen] = useState(false);
  const close = () => setOpen(false);

  // Popover (not DropdownMenu) — base-ui's Menu primitive was the only
  // thing in the app using that API surface; it had a hydration/portal
  // edge case that rendered the Next.js "This page couldn't load"
  // splash on first open. ServersPopover uses Popover successfully on
  // every page so we mirror that pattern here. Each row is a plain
  // <Link> or <button>, no special focus-trap logic — the popover
  // closes via outside-click.
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        aria-label="Account menu"
        className="ml-0.5 inline-flex size-8 items-center justify-center rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--focus)]"
      >
        <Avatar className="size-7">
          {user?.image && <AvatarImage src={user.image} alt={user.name ?? ""} />}
          <AvatarFallback className="bg-[var(--bg-tertiary)] text-xs font-medium text-[var(--text-secondary)]">
            {initial}
          </AvatarFallback>
        </Avatar>
      </PopoverTrigger>
      {/* Personal-scope rows plus one way into /settings; cluster-wide
          knobs live on /settings, not here. */}
      <PopoverContent align="end" className="w-60 gap-0 p-1">
        <div className="mb-1 border-b border-[var(--border-subtle)] px-2 pt-1 pb-2">
          <p className="truncate text-sm font-medium">{user?.name ?? "User"}</p>
          {user?.email && (
            <p className="truncate text-xs text-[var(--text-tertiary)]">{user.email}</p>
          )}
        </div>
        <MenuRow href="/settings/profile" icon={UserIcon} onNavigate={close}>Profile</MenuRow>
        <MenuRow href="/settings/tokens" icon={KeyRound} onNavigate={close}>API tokens</MenuRow>
        <div className="my-1 h-px bg-[var(--border-subtle)]" />
        <MenuRow href="/settings" icon={Settings} onNavigate={close}>Settings</MenuRow>
        {canAdmin && <MenuRow href="/settings/users" icon={Users} onNavigate={close}>Users &amp; groups</MenuRow>}
        <div className="my-1 h-px bg-[var(--border-subtle)]" />
        <ThemeRow />
        <div className="my-1 h-px bg-[var(--border-subtle)]" />
        <button
          type="button"
          onClick={() => signOut()}
          className="flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left text-sm text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
        >
          <LogOut className="size-4" aria-hidden />
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
  onNavigate,
  children,
}: {
  href: string;
  icon: React.ComponentType<{ className?: string }>;
  onNavigate: () => void;
  children: React.ReactNode;
}) {
  return (
    <Link
      href={href}
      onClick={onNavigate}
      className="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
    >
      <Icon className="size-4" aria-hidden />
      {children}
    </Link>
  );
}

// ThemeRow: a three-way theme choice inside the account menu, so phones
// (where the bar's theme button is hidden) can still switch.
function ThemeRow() {
  const { theme, setTheme } = useTheme();
  const options = [
    { value: "light", label: "Light", Icon: Sun },
    { value: "dark", label: "Dark", Icon: Moon },
    { value: "system", label: "System", Icon: Monitor },
  ] as const;
  return (
    <div className="flex items-center justify-between gap-2 px-2 py-1">
      <span className="text-sm text-[var(--text-secondary)]">Theme</span>
      <div role="radiogroup" aria-label="Theme" className="flex rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-0.5">
        {options.map(({ value, label, Icon }) => (
          <button
            key={value}
            type="button"
            role="radio"
            aria-checked={theme === value}
            aria-label={label}
            title={label}
            onClick={() => setTheme(value)}
            className="inline-flex size-6 items-center justify-center rounded text-[var(--text-tertiary)] hover:text-[var(--text-primary)] aria-checked:bg-[var(--bg-elevated)] aria-checked:text-[var(--text-primary)] aria-checked:shadow-[var(--shadow-sm)]"
          >
            <Icon className="size-3.5" aria-hidden />
          </button>
        ))}
      </div>
    </div>
  );
}

