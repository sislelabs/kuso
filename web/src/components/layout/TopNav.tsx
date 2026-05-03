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
} from "lucide-react";
import { ServersPopover } from "./ServersPopover";

// TopNav is the persistent shell across every authenticated page. Left
// side: kuso logomark, then breadcrumb-style project picker + (when on
// a project) the environment switcher. Right side: servers, theme,
// notifications, user menu. The whole bar is `h-12 sticky` to leave
// room below for the page's own toolbar.
export function TopNav() {
  const params = useRouteParams<{ project: string }>(["project"]);
  const currentProject = params.project ?? "";

  return (
    <header
      role="navigation"
      className="sticky top-0 z-40 flex h-12 shrink-0 items-center gap-1.5 border-b border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 lg:px-4"
    >
      <Link href="/projects" aria-label="Home" className="mr-1.5 flex shrink-0 items-center">
        <Logo />
      </Link>

      <Crumb>
        <ProjectPicker currentProject={currentProject} />
      </Crumb>

      {currentProject && (
        <Crumb>
          <EnvironmentSwitcher project={currentProject} />
        </Crumb>
      )}

      <div className="flex-1" />

      <ServersPopover />
      <ThemeToggle />
      <NotificationsButton />
      <UserMenu />
    </header>
  );
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
      <PopoverContent align="start" className="w-72 p-0">
        <Command>
          <CommandInput placeholder="Find a project…" className="h-9" />
          <CommandList>
            <CommandEmpty>No projects.</CommandEmpty>
            <CommandGroup heading="Projects">
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
                    className="flex items-center gap-2"
                  >
                    <span className="truncate font-medium">{name}</span>
                    {active && <Check className="ml-auto h-3.5 w-3.5 text-[var(--accent)]" />}
                  </CommandItem>
                );
              })}
            </CommandGroup>
            <CommandGroup>
              <CommandItem
                value="__new__"
                onSelect={() => {
                  setOpen(false);
                  router.push("/projects/new");
                }}
                className="flex items-center gap-2 text-[var(--accent)]"
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
      <PopoverContent align="start" className="w-64 p-0">
        <Command>
          <CommandInput placeholder="Switch environment…" className="h-9" />
          <CommandList>
            <CommandEmpty>No environments yet.</CommandEmpty>
            {envs.length > 0 && (
              <CommandGroup heading="Environments">
                {envs.map((e) => {
                  const short = labelFor(e.metadata.name);
                  const active = (currentEnv === "production" && e.spec.kind === "production") || currentEnv === short;
                  return (
                    <CommandItem
                      key={e.metadata.uid ?? e.metadata.name}
                      value={short}
                      onSelect={() => setEnv(e.spec.kind === "production" ? "production" : short)}
                      className="flex items-center gap-2"
                    >
                      <span
                        className={cn(
                          "inline-block h-1.5 w-1.5 rounded-full",
                          e.spec.kind === "production"
                            ? "bg-emerald-400"
                            : "bg-amber-400"
                        )}
                      />
                      <span className="truncate font-mono text-xs">{short}</span>
                      {e.spec.kind === "preview" && (
                        <span className="ml-auto rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
                          preview
                        </span>
                      )}
                      {active && <Check className="ml-1 h-3.5 w-3.5 text-[var(--accent)]" />}
                    </CommandItem>
                  );
                })}
              </CommandGroup>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
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
            <Link href="/settings/config" className="flex items-center gap-2">
              <Settings className="h-3.5 w-3.5" />
              Cluster config
            </Link>
          }
        />
        <DropdownMenuItem
          render={
            <Link href="/settings/nodes" className="flex items-center gap-2">
              <Server className="h-3.5 w-3.5" />
              Nodes
            </Link>
          }
        />
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
