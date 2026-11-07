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
import { useProjects, useServices, useAddons, useEnvironments } from "@/features/projects";
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
  Play,
  ScrollText,
  Variable,
  Clock,
  Bell,
  Store,
} from "lucide-react";
import { buildTriggerMessage, triggerBuild } from "@/features/services";
import { serviceShortName } from "@/lib/utils";
import { ENV_GROUP_LABEL, envGroupName } from "@/lib/env-group";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";

// Pull the current project name out of the pathname when we're on a
// /projects/<name>/... route. That lets the palette load the
// per-project services/addons without requiring the caller to plumb
// it through.
function currentProjectFromPath(pathname: string | null): string {
  if (!pathname) return "";
  const m = pathname.match(/^\/projects\/([^/?]+)/);
  // /projects/new is the create page, not a project called "new".
  return m && m[1] !== "new" ? decodeURIComponent(m[1]) : "";
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
  const envs = useEnvironments(open && currentProject ? currentProject : "");

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
  // Both pages sit behind settings:admin (settings index cards); listing
  // them for everyone just leads to a load error.
  const isSettingsAdmin = perms.includes("settings:admin");

  const addonList = addons.data ?? [];
  // An env-group clone is its own KusoService CR ("<project>-web-staging")
  // labelled with its group. It opens in the overlay as ?service=web-staging
  // &env=staging, and redeploys from its own env's branch. Shared services
  // carry no label and resolve to production here.
  const serviceList = useMemo(() => {
    const envList = envs.data ?? [];
    return (services.data ?? []).map((s) => {
      const name = serviceShortName(currentProject, s.metadata.name);
      const label = s.metadata.labels?.[ENV_GROUP_LABEL];
      const group = label && label !== "production" ? label : "production";
      const base = group !== "production" && name.endsWith(`-${group}`) ? name.slice(0, -(group.length + 1)) : name;
      const env = envList.find((e) => e.spec.service === s.metadata.name && envGroupName(e) === group);
      return {
        svc: s,
        name,
        group,
        display: group === "production" ? name : `${base} (${group})`,
        query: group === "production" ? `service=${name}` : `service=${name}&env=${encodeURIComponent(group)}`,
        branch: group === "production" ? undefined : env?.spec.branch,
      };
    });
  }, [services.data, envs.data, currentProject]);
  type PaletteService = (typeof serviceList)[number];
  // A redeploy is a real build + rollout: confirm instead of firing on Enter.
  const [pendingRedeploy, setPendingRedeploy] = useState<PaletteService | null>(null);

  // Per-service env-var index. cmdk's value-string matching means a
  // user typing "DATABASE_URL" lands on the right service row even
  // though the literal isn't in metadata.name — we cram the env-var
  // keys into the value string and surface them as their own
  // CommandGroup so the result reads as "DATABASE_URL → web".
  // service is the SHORT name (metadata.name is the CR FQN
  // "<project>-web"; the ?service= param and every /services/:s API
  // endpoint take "web").
  const envVarRows = useMemo(() => {
    const rows: { service: string; display: string; query: string; key: string }[] = [];
    for (const { svc, name, display, query } of serviceList) {
      for (const v of svc.spec.envVars ?? []) {
        if (v.name) rows.push({ service: name, display, query, key: v.name });
      }
    }
    return rows;
  }, [serviceList]);

  const runBuild = async ({ name, display, branch }: PaletteService) => {
    setPendingRedeploy(null);
    try {
      const res = await triggerBuild(currentProject, name, branch ? { branch } : {});
      toast.success(buildTriggerMessage(res, `Redeploy started for ${display}`, name));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to trigger build");
    }
  };

  return (
    <>
    <ConfirmDialog
      open={pendingRedeploy != null}
      title={`Redeploy ${pendingRedeploy?.display ?? ""}?`}
      body={`Starts a new ${pendingRedeploy?.group ?? "production"} build from ${
        pendingRedeploy?.branch ? `branch ${pendingRedeploy.branch}` : "the tracked branch"
      } and rolls the service when it's green.`}
      confirmLabel="Redeploy"
      destructive={false}
      onCancel={() => setPendingRedeploy(null)}
      onConfirm={() => {
        if (pendingRedeploy) void runBuild(pendingRedeploy);
      }}
    />
    <CommandDialog open={open} onOpenChange={setOpen}>
      <CommandInput placeholder="Jump or do: project, service, env var, redeploy…" />
      <CommandList>
        <CommandEmpty>No matches.</CommandEmpty>

        {currentProject && (
          <>
            <CommandGroup heading={`Add to ${currentProject}`}>
              <CommandItem
                onSelect={() => go(`/projects/${encodeURIComponent(currentProject)}/services/new`)}
                value="add new service create"
              >
                <Plus className="h-4 w-4 text-[var(--text-tertiary)]" />
                Add service
              </CommandItem>
              {/* The canvas opens these dialogs from ?add=; it isn't
                  mounted on an empty project, which has its own buttons. */}
              {serviceList.length + addonList.length > 0 && (
                <>
                  <CommandItem
                    onSelect={() => go(`/projects/${encodeURIComponent(currentProject)}?add=addon`)}
                    value="add new addon database postgres redis create"
                  >
                    <Database className="h-4 w-4 text-[var(--text-tertiary)]" />
                    Add addon
                  </CommandItem>
                  <CommandItem
                    onSelect={() => go(`/projects/${encodeURIComponent(currentProject)}?add=cron`)}
                    value="add new cron job schedule create"
                  >
                    <Clock className="h-4 w-4 text-[var(--text-tertiary)]" />
                    Add cron job
                  </CommandItem>
                </>
              )}
            </CommandGroup>
            <CommandSeparator />
          </>
        )}

        {currentProject && serviceList.length > 0 && (
          <>
            {/* metadata.name is the CR FQN ("<project>-web"); the
                ?service= param and the build API both take the SHORT
                name ("web") — passing the FQN 404s. */}
            <CommandGroup heading={`Services in ${currentProject}`}>
              {serviceList.map(({ svc: s, name, display, query }) => {
                return (
                  <CommandItem
                    key={s.metadata.uid ?? name}
                    onSelect={() => go(`/projects/${currentProject}?${query}`)}
                    value={`service ${display} ${name} ${s.spec.runtime ?? ""} ${s.spec.repo ?? ""}`}
                  >
                    <Server className="h-4 w-4 text-[var(--text-tertiary)]" />
                    <span>{display}</span>
                    <CommandShortcut>{s.spec.runtime ?? ""}</CommandShortcut>
                  </CommandItem>
                );
              })}
            </CommandGroup>
            <CommandSeparator />

            {/* Service actions — power users hit cmd-K + "redeploy api"
                instead of clicking through to the canvas and finding
                the Trigger button. Same for tailing logs. Image
                services never build (the server 400s the build
                endpoint) — they redeploy by bumping the image tag in
                Settings, so they get no Redeploy row. */}
            <CommandGroup heading="Service actions">
              {serviceList
                .filter((p) => p.svc.spec.runtime !== "image")
                .map((p) => {
                  return (
                    <CommandItem
                      key={`redeploy-${p.name}`}
                      onSelect={() => {
                        setOpen(false);
                        setPendingRedeploy(p);
                      }}
                      value={`redeploy build trigger ${p.display} ${p.name}`}
                    >
                      <Play className="h-4 w-4 text-[var(--text-tertiary)]" />
                      <span>Redeploy {p.display}</span>
                    </CommandItem>
                  );
                })}
              {serviceList.map(({ name, display, query }) => {
                return (
                  <CommandItem
                    key={`logs-${name}`}
                    onSelect={() => go(`/projects/${currentProject}?${query}&tab=logs`)}
                    value={`logs tail ${display} ${name}`}
                  >
                    <ScrollText className="h-4 w-4 text-[var(--text-tertiary)]" />
                    <span>Tail logs · {display}</span>
                  </CommandItem>
                );
              })}
            </CommandGroup>
            <CommandSeparator />

            {/* Env-var index. The user types DATABASE_URL and lands
                on the Variables tab of the right service with no
                round-trip through the overlay tabs. */}
            {envVarRows.length > 0 && (
              <>
                <CommandGroup heading="Env vars">
                  {envVarRows.map(({ service, display, query, key }) => (
                    <CommandItem
                      key={`env-${service}-${key}`}
                      onSelect={() => go(`/projects/${currentProject}?${query}&tab=variables`)}
                      value={`env variable ${key} ${display} ${service}`}
                    >
                      <Variable className="h-4 w-4 text-[var(--text-tertiary)]" />
                      <span className="font-mono">{key}</span>
                      <CommandShortcut>{display}</CommandShortcut>
                    </CommandItem>
                  ))}
                </CommandGroup>
                <CommandSeparator />
              </>
            )}
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
          </CommandItem>
          <CommandItem onSelect={() => go("/marketplace")} value="marketplace apps templates one-click deploy">
            <Store className="h-4 w-4 text-[var(--text-tertiary)]" />
            Marketplace
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
          {isSettingsAdmin && (
            <CommandItem onSelect={() => go("/settings/nodes")} value="nodes cluster servers">
              <Box className="h-4 w-4 text-[var(--text-tertiary)]" />
              Cluster nodes
            </CommandItem>
          )}
          {isSettingsAdmin && (
            <CommandItem onSelect={() => go("/settings/alerts")} value="alerts alerting rules thresholds">
              <Bell className="h-4 w-4 text-[var(--text-tertiary)]" />
              Alerts
            </CommandItem>
          )}
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
    </>
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
      className="inline-flex items-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-1.5 text-xs text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-secondary)] transition-colors"
      aria-label="Open command palette"
    >
      <Search className="h-3.5 w-3.5" />
      <span className="hidden sm:inline">Search</span>
      <kbd className="ml-2 hidden sm:inline rounded border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
        ⌘K
      </kbd>
    </button>
  );
}
