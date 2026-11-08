"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import { AlertTriangle, ChevronRight, HardDrive, Package, Plug, Server, Users } from "lucide-react";
import { StatusDot } from "@/components/ui/status-pill";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import { useDrains } from "@/features/drains";
import { useSetupStatus } from "@/features/github";
import { addonBackupCoverageText, addonBackupFailingText, summarizeAddonBackups } from "@/components/health/backupCoverage";
import { useSystemVersion } from "@/features/system-version";
import { countTeam, pendingInviteCount, type InviteRow } from "@/features/users";
import { api } from "@/lib/api-client";
import type { StatusMeta, StatusTone } from "@/lib/status";

// The settings landing page is a status board, not a second copy of the
// sidebar: each tile answers "is this part of the cluster OK?" and links
// to the page that fixes it. Tiles the viewer can't read aren't shown
// (and their requests never fire).

interface NodeRow {
  ready: boolean;
  schedulable: boolean;
  unreachable?: boolean;
}
interface HealthEntry {
  healthy: boolean;
  detail: string;
}
interface BackupHealth {
  backup: HealthEntry;
  registryGC: HealthEntry;
  addonBackups?: { covered: boolean; healthy: boolean }[];
}
interface UserRow {
  isActive: boolean;
  lastLogin?: string | null;
  provider?: string;
}

interface TileState {
  status: StatusMeta;
  value: string;
  detail?: string;
  // A caveat that doesn't change the headline (e.g. "no failures, but
  // some addons aren't backed up"): own line, amber icon.
  detailTone?: "warning";
}

const meta = (tone: StatusTone, label: string): StatusMeta => ({ tone, label });

function plural(n: number, one: string, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`;
}

function Tile({
  href,
  icon,
  title,
  query,
  render,
}: {
  href: string;
  icon: ReactNode;
  title: string;
  query: Pick<UseQueryResult<unknown>, "isPending" | "isError">;
  render: () => TileState;
}) {
  const state: TileState | null = query.isPending
    ? null
    : query.isError
      ? { status: meta("neutral", "Unknown"), value: "Couldn't load", detail: "Open the page for details." }
      : render();
  return (
    <li className="bg-[var(--bg-secondary)]">
      <Link
        href={href}
        className="group flex h-full items-start gap-3 px-4 py-4 transition-colors hover:bg-[var(--bg-tertiary)]/50"
      >
        <span className="mt-0.5 inline-flex size-8 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] group-hover:text-[var(--text-primary)] [&_svg]:size-4">
          {icon}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-xs font-medium text-[var(--text-secondary)]">{title}</span>
          {state ? (
            <>
              <span className="mt-1 flex items-center gap-2 text-sm font-medium text-[var(--text-primary)]">
                <StatusDot status={state.status} />
                <span className="sr-only">{state.status.label}: </span>
                <span className="truncate">{state.value}</span>
              </span>
              {state.detail &&
                (state.detailTone === "warning" ? (
                  <span className="mt-1 flex items-start gap-1.5 text-xs leading-relaxed text-[var(--text-secondary)]">
                    <AlertTriangle aria-hidden className="mt-0.5 size-3.5 shrink-0 text-[var(--warning)]" />
                    {state.detail}
                  </span>
                ) : (
                  <span className="mt-0.5 block text-xs leading-relaxed text-[var(--text-secondary)]">{state.detail}</span>
                ))}
            </>
          ) : (
            <>
              <Skeleton className="mt-1.5 h-4 w-28" />
              <Skeleton className="mt-1.5 h-3 w-40" />
            </>
          )}
        </span>
        <ChevronRight aria-hidden className="mt-2 size-4 shrink-0 text-[var(--text-tertiary)]" />
      </Link>
    </li>
  );
}

function NodesTile() {
  const nodes = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeRow[]>("/api/kubernetes/nodes"),
    staleTime: 15_000,
  });
  return (
    <Tile
      href="/settings/nodes"
      icon={<Server />}
      title="Nodes"
      query={nodes}
      render={() => {
        const list = nodes.data ?? [];
        const ready = list.filter((n) => n.ready && !n.unreachable).length;
        const cordoned = list.filter((n) => !n.schedulable).length;
        const down = list.length - ready;
        return {
          status: down > 0 ? meta("danger", "Degraded") : cordoned > 0 ? meta("warning", "Cordoned") : meta("success", "Ready"),
          value: `${ready} of ${plural(list.length, "node")} ready`,
          detail:
            down > 0
              ? `${plural(down, "node")} not ready. Workloads on ${down === 1 ? "it" : "them"} may be down.`
              : cordoned > 0
                ? `${plural(cordoned, "node")} cordoned: no new pods land there.`
                : "Every node is accepting work.",
        };
      }}
    />
  );
}

function BackupsTile() {
  const health = useQuery({
    queryKey: ["admin", "backup-health"],
    queryFn: () => api<BackupHealth>("/api/admin/backup-health"),
    staleTime: 60_000,
  });
  return (
    <Tile
      href="/settings/backups"
      icon={<HardDrive />}
      title="Backups"
      query={health}
      render={() => {
        const h = health.data;
        const c = summarizeAddonBackups(h?.addonBackups ?? []);
        const { failing, uncovered } = c;
        const cpOK = h?.backup.healthy ?? false;
        if (failing > 0 || !cpOK) {
          return {
            status: meta("danger", "Failing"),
            value: addonBackupFailingText(c) ?? "Control-plane backup isn't healthy",
            detail: !cpOK ? h?.backup.detail : undefined,
          };
        }
        return {
          // The headline says nothing is failing, so its dot is green; the
          // coverage gap is the amber caveat underneath.
          status: meta("success", "Healthy"),
          detailTone: uncovered > 0 ? "warning" : undefined,
          value: uncovered > 0 ? "No failing backups" : "Backups healthy",
          detail:
            uncovered > 0
              ? `${addonBackupCoverageText(c)}.`
              : "Control plane and every addon back up on schedule.",
        };
      }}
    />
  );
}

function UpdatesTile() {
  const version = useSystemVersion();
  return (
    <Tile
      href="/settings/updates"
      icon={<Package />}
      title="kuso version"
      query={version}
      render={() => {
        const v = version.data;
        if (v?.needsUpdate) {
          return {
            status: meta("info", "Update available"),
            value: `${v.latest} available`,
            detail: `Running ${v.current}. Nothing installs until you press Update.`,
          };
        }
        if (v?.checksDisabled) {
          return { status: meta("neutral", "Checks off"), value: `Running ${v.current}`, detail: "Update checks are turned off on this server." };
        }
        return { status: meta("success", "Current"), value: `Up to date`, detail: v ? `Running ${v.current}.` : undefined };
      }}
    />
  );
}

function UsersTile() {
  const users = useQuery({
    queryKey: ["admin", "users"],
    queryFn: async () => {
      const res = await api<{ data?: UserRow[] } | UserRow[]>("/api/users");
      return Array.isArray(res) ? res : (res.data ?? []);
    },
  });
  const invites = useQuery({
    queryKey: ["admin", "invites"],
    queryFn: () => api<InviteRow[]>("/api/invites"),
  });
  return (
    <Tile
      href="/settings/users"
      icon={<Users />}
      title="Team"
      query={users}
      render={() => {
        const list = users.data ?? [];
        const team = countTeam(list);
        const pending = pendingInviteCount(invites.data);
        return {
          status: meta("neutral", "Team"),
          value: plural(team.active, "active user"),
          detail: [
            team.invited > 0 ? `${team.invited} never signed in` : null,
            team.disabled > 0 ? `${team.disabled} disabled` : null,
            pending > 0 ? `${plural(pending, "invite")} pending` : null,
          ]
            .filter(Boolean)
            .join(" · ") || undefined,
        };
      }}
    />
  );
}

function IntegrationsTile() {
  const github = useSetupStatus();
  const channels = useQuery({
    queryKey: ["admin", "notifications"],
    queryFn: async () => {
      const res = await api<{ data?: unknown[] } | unknown[]>("/api/notifications");
      return Array.isArray(res) ? res : (res.data ?? []);
    },
  });
  const drains = useDrains(true);
  return (
    <Tile
      href="/settings/github"
      icon={<Plug />}
      title="Integrations"
      query={github}
      render={() => {
        const gh = github.data?.configured ?? false;
        const nCh = channels.data?.length;
        const nDr = drains.data?.length;
        return {
          status: gh ? meta("success", "Connected") : meta("warning", "Not connected"),
          value: gh ? "GitHub App connected" : "GitHub App not connected",
          detail:
            [
              nCh === undefined ? null : nCh === 0 ? "no notification channels" : plural(nCh, "notification channel"),
              nDr === undefined || nDr === 0 ? null : plural(nDr, "log drain"),
            ]
              .filter(Boolean)
              .join(" · ")
              .replace(/^./, (c) => c.toUpperCase()) || undefined,
        };
      }}
    />
  );
}

export function SettingsOverview() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const canUsers = useCan(Perms.UserWrite);
  if (!isAdmin && !canUsers) return null;
  return (
    <section aria-labelledby="settings-status">
      <h2 id="settings-status" className="mb-3 text-sm font-semibold text-[var(--text-primary)]">
        Cluster status
      </h2>
      <ul className="grid gap-px overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--border-subtle)] sm:grid-cols-2 sm:[&>li:last-child:nth-child(odd)]:col-span-2">
        {isAdmin && <NodesTile />}
        {isAdmin && <BackupsTile />}
        {isAdmin && <UpdatesTile />}
        {canUsers && <UsersTile />}
        {isAdmin && <IntegrationsTile />}
      </ul>
    </section>
  );
}
