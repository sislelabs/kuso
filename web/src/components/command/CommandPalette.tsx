"use client";

import { useEffect, useMemo, useState } from "react";
import { useRouter, usePathname } from "next/navigation";
import { useTheme } from "next-themes";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
  CommandShortcut,
} from "@/components/ui/command";
import { useProjects, useServices, useAddons } from "@/features/projects";
import { useSession, useSignOut } from "@/features/auth";
import {
  LayoutGrid,
  Plus,
  Settings,
  KeyRound,
  Sun,
  Moon,
  LogOut,
  ExternalLink,
  Search,
  User,
  Database,
  Server,
  Box,
} from "lucide-react";

// Pull the current project name out of the pathname when we're on a
// /projects/<name>/... route. That lets the palette load the
// per-project services/addons without requiring the caller to plumb
// it through.
function currentProjectFromPath(pathname: string | null): string {
  if (!pathname) return "";
  const m = pathname.match(/^\/projects\/([^/?]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

export function CommandPalette() {
  const [open, setOpen] = useState(false);
  const router = useRouter();
  const pathname = usePathname();
  const projects = useProjects();
  const { data: session } = useSession();
  const signOut = useSignOut();
  const { theme, setTheme } = useTheme();
  const currentProject = useMemo(() => currentProjectFromPath(pathname), [pathname]);
  // Per-project context — only fetches when the palette is open AND
  // the user is on a project page. Avoids burning cycles polling
  // services/addons in the background for a feature most people use
  // a few times a day.
  const services = useServices(open && currentProject ? currentProject : "");
  const addons = useAddons(open && currentProject ? currentProject : "");

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.key === "k" || e.key === "K") && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setOpen((v) => !v);
      } else if (e.key === "Escape") {
        setOpen(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const go = (path: string) => {
    setOpen(false);
    router.push(path);
  };

  const perms = session?.session.permissions ?? [];
  const isAdmin = perms.includes("user:write");

  const serviceList = services.data ?? [];
  const addonList = addons.data ?? [];

  return (
    <CommandDialog open={open} onOpenChange={setOpen}>
      <CommandInput placeholder="Search projects, services, addons, settings…" />
      <CommandList>
        <CommandEmpty>No matches.</CommandEmpty>

        {currentProject && serviceList.length > 0 && (
          <>
            <CommandGroup heading={`Services in ${currentProject}`}>
              {serviceList.map((s) => {
                const name = s.metadata.name;
                return (
                  <CommandItem
                    key={s.metadata.uid ?? name}
                    onSelect={() => go(`/projects/${currentProject}?service=${name}`)}
                    value={`service ${name} ${s.spec.runtime ?? ""} ${s.spec.repo ?? ""}`}
                  >
                    <Server className="h-4 w-4 text-[var(--text-tertiary)]" />
                    <span>{name}</span>
                    <CommandShortcut>{s.spec.runtime ?? ""}</CommandShortcut>
                  </CommandItem>
                );
              })}
            </CommandGroup>
            <CommandSeparator />
          </>
        )}

        {currentProject && addonList.length > 0 && (
          <>
            <CommandGroup heading={`Addons in ${currentProject}`}>
              {addonList.map((a) => {
                const name = a.metadata.name;
                return (
                  <CommandItem
                    key={a.metadata.uid ?? name}
                    onSelect={() => go(`/projects/${currentProject}?addon=${name}`)}
                    value={`addon ${name} ${a.spec.kind ?? ""}`}
                  >
                    <Database className="h-4 w-4 text-[var(--text-tertiary)]" />
                    <span>{name}</span>
                    <CommandShortcut>{a.spec.kind ?? ""}</CommandShortcut>
                  </CommandItem>
                );
              })}
            </CommandGroup>
            <CommandSeparator />
          </>
        )}

        <CommandGroup heading="Projects">
          {(projects.data ?? []).map((p) => (
            <CommandItem
              key={p.metadata.uid ?? p.metadata.name}
              onSelect={() => go(`/projects/${p.metadata.name}`)}
              value={`project ${p.metadata.name} ${p.spec.description ?? ""}`}
            >
              <LayoutGrid className="h-4 w-4 text-[var(--text-tertiary)]" />
              <span>{p.metadata.name}</span>
              <CommandShortcut>{p.spec.description ?? ""}</CommandShortcut>
            </CommandItem>
          ))}
          <CommandItem onSelect={() => go("/projects/new")} value="new project create">
            <Plus className="h-4 w-4 text-[var(--text-tertiary)]" />
            New project
          </CommandItem>
        </CommandGroup>

        <CommandSeparator />

        <CommandGroup heading="Navigation">
          <CommandItem onSelect={() => go("/projects")} value="all projects list dashboard home">
            <LayoutGrid className="h-4 w-4 text-[var(--text-tertiary)]" />
            All projects
            <CommandShortcut>g p</CommandShortcut>
          </CommandItem>
          <CommandItem onSelect={() => go("/settings")} value="settings index">
            <Settings className="h-4 w-4 text-[var(--text-tertiary)]" />
            Settings
          </CommandItem>
          <CommandItem onSelect={() => go("/settings/profile")} value="profile settings">
            <User className="h-4 w-4 text-[var(--text-tertiary)]" />
            Profile
          </CommandItem>
          <CommandItem onSelect={() => go("/settings/tokens")} value="tokens api access cli pat">
            <KeyRound className="h-4 w-4 text-[var(--text-tertiary)]" />
            API tokens
          </CommandItem>
          <CommandItem onSelect={() => go("/settings/nodes")} value="nodes cluster servers">
            <Box className="h-4 w-4 text-[var(--text-tertiary)]" />
            Cluster nodes
          </CommandItem>
          {isAdmin && (
            <CommandItem onSelect={() => go("/settings/users")} value="users admin">
              <Settings className="h-4 w-4 text-[var(--text-tertiary)]" />
              Users (admin)
            </CommandItem>
          )}
        </CommandGroup>

        <CommandSeparator />

        <CommandGroup heading="Actions">
          <CommandItem
            onSelect={() => {
              setTheme(theme === "dark" ? "light" : "dark");
              setOpen(false);
            }}
            value="toggle theme dark light"
          >
            {theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            Toggle theme
          </CommandItem>
          <CommandItem
            onSelect={() => {
              setOpen(false);
              window.open("https://github.com/sislelabs/kuso/blob/main/docs", "_blank");
            }}
            value="docs documentation"
          >
            <ExternalLink className="h-4 w-4 text-[var(--text-tertiary)]" />
            Open docs
          </CommandItem>
          <CommandItem
            onSelect={() => {
              setOpen(false);
              signOut();
            }}
            value="sign out logout"
          >
            <LogOut className="h-4 w-4 text-[var(--text-tertiary)]" />
            Sign out
          </CommandItem>
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  );
}

export function CommandTrigger() {
  return (
    <button
      type="button"
      onClick={() => {
        const evt = new KeyboardEvent("keydown", { key: "k", metaKey: true });
        window.dispatchEvent(evt);
      }}
      className="hidden sm:inline-flex items-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-1.5 text-xs text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-secondary)] transition-colors"
      aria-label="Open command palette"
    >
      <Search className="h-3.5 w-3.5" />
      <span>Search</span>
      <kbd className="ml-2 rounded border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
        ⌘K
      </kbd>
    </button>
  );
}
