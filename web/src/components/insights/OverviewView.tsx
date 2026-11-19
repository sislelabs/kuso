"use client";

import { useMemo, type ReactNode } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, CircleAlert } from "lucide-react";
import { api } from "@/lib/api-client";
import { relativeTime } from "@/lib/format";
import { SERVICE_STATUS } from "@/lib/status";
import { cn } from "@/lib/utils";
import { useCan, Perms } from "@/features/auth";
import { useProjects, useProjectsSummary, type ProjectSummaryItem } from "@/features/projects";
import type { Insights } from "@/features/insights";
import type { NodeSummary } from "@/components/layout/ServersPopover";
import { nodePressures } from "@/components/layout/nodeHealth";
import { attentionItems } from "@/components/project/list/fleetModel";
import { buildCardModel, serviceShortName } from "@/components/project/list/projectCardModel";
import { useFleetBackups, useFleetUptime, useFleetVulns, type FleetVulns } from "@/components/project/list/fleetPanels";
import { Skeleton } from "@/components/ui/skeleton";
import { metricDef, serviceLabel, type MetricKey } from "./insightsModel";
import {
  compareHref,
  findOutliers,
  findWaste,
  liveFailedBuilds,
  recentChanges,
  serviceHref,
  type AttentionRow,
  type Tone,
} from "./overviewModel";

const panel = "rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-secondary)]";
const h2 = "font-heading text-[15px] font-semibold text-[var(--text-primary)]";
const rowLink =
  "grid w-full items-center gap-3 px-4 py-2.5 text-left text-[13px] outline-none hover:bg-[var(--bg-tertiary)] focus-visible:bg-[var(--bg-tertiary)]";

function ToneIcon({ tone }: { tone: Tone }) {
  const Icon = tone === "danger" ? CircleAlert : AlertTriangle;
  return <Icon aria-hidden className={cn("size-4 shrink-0", tone === "danger" ? "text-[var(--danger)]" : "text-[var(--warning)]")} />;
}

function Section({ title, count, children, className }: { title: string; count?: number; children: ReactNode; className?: string }) {
  return (
    <section className={className}>
      <h2 className={cn(h2, "mb-2.5 flex items-baseline gap-2")}>
        {title}
        {count !== undefined && count > 0 && <span className="font-sans text-xs font-normal text-[var(--text-tertiary)]">{count}</span>}
      </h2>
      <div className={cn(panel, "overflow-hidden")}>{children}</div>
    </section>
  );
}

function Empty({ children }: { children: ReactNode }) {
  return (
    <p className="flex items-center gap-2 px-4 py-4 text-[13px] text-[var(--text-secondary)]">
      <CheckCircle2 aria-hidden className="size-4 shrink-0 text-[var(--success)]" />
      {children}
    </p>
  );
}

// publicServiceIds are services whose production env has a host, so the
// waste check can tell an idle site from a worker with no ingress.
function publicServiceIds(summary: readonly ProjectSummaryItem[]): Set<string> {
  const out = new Set<string>();
  for (const item of summary) {
    const project = item.project.metadata.name;
    for (const svc of item.services) {
      const fqn = svc.metadata.name;
      const env = item.environments.find((e) => e.spec.service === fqn && e.spec.kind === "production");
      if (env?.spec.host || (svc.spec.domains?.length ?? 0) > 0) out.add(`${project}/${serviceShortName(svc)}`);
    }
  }
  return out;
}

export function OverviewView({ data, dim }: { data: Insights; dim: boolean }) {
  const projects = useProjects();
  const summary = useProjectsSummary();
  const isAdmin = useCan(Perms.SettingsAdmin);
  const names = useMemo(() => (projects.data ?? []).map((p) => p.metadata.name), [projects.data]);
  const vulns = useFleetVulns(names, true);
  const uptime = useFleetUptime(names, true);
  const backups = useFleetBackups(isAdmin);
  const nodes = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
    refetchInterval: 30_000,
    staleTime: 15_000,
    enabled: isAdmin,
  });

  const attention = useMemo((): AttentionRow[] => {
    const rows: AttentionRow[] = [];
    const byName = new Map((summary.data ?? []).map((i) => [i.project.metadata.name, i]));
    const models = (projects.data ?? []).map((p) => buildCardModel(p, byName.get(p.metadata.name)));
    for (const a of attentionItems(models)) {
      if (a.tone !== "danger" && a.tone !== "warning") continue;
      rows.push({
        tone: a.tone,
        kind: SERVICE_STATUS[a.status].label,
        title: a.project === a.service ? a.project : `${a.project} / ${a.service}`,
        detail: a.detail,
        href: serviceHref(a.project, a.service),
      });
    }
    for (const u of uptime?.bad ?? []) {
      rows.push({ tone: "danger", kind: "Down", title: u.project === u.service ? u.project : `${u.project} / ${u.service}`, detail: "uptime check failing", href: serviceHref(u.project, u.service) });
    }
    for (const b of liveFailedBuilds(data)) {
      rows.push({
        tone: "warning",
        kind: b.status === "release-failed" ? "Release failed" : "Build failed",
        title: serviceLabel(b),
        detail: [b.error || b.commitMessage, relativeTime(b.at)].filter(Boolean).join(" · "),
        href: serviceHref(b.project, b.service),
      });
    }
    for (const c of data.certProblems) {
      rows.push({ tone: c.problem.startsWith("expires in") ? "warning" : "danger", kind: "Certificate", title: c.host, detail: c.problem, href: `/projects/${encodeURIComponent(c.project)}` });
    }
    if (backups) {
      if (!backups.controlPlaneHealthy) rows.push({ tone: "danger", kind: "Backups", title: "Control-plane backup failing", href: "/settings/backups" });
      if (backups.failing > 0) rows.push({ tone: "danger", kind: "Backups", title: `${backups.failing} addon ${backups.failing === 1 ? "backup is" : "backups are"} failing`, href: "/settings/backups#addon-backups" });
      if (backups.uncovered > 0) rows.push({ tone: "warning", kind: "Backups", title: `${backups.uncovered} of ${backups.total} addons have no backup schedule`, href: "/settings/backups#addon-backups" });
    }
    for (const n of nodes.data ?? []) {
      if (!n.ready) rows.push({ tone: "danger", kind: "Server", title: n.name, detail: "not ready", href: "/settings/nodes" });
      else for (const p of nodePressures(n)) rows.push({ tone: "warning", kind: "Server", title: n.name, detail: p.label, href: "/settings/nodes" });
    }
    for (const f of vulns?.failedScans ?? []) {
      rows.push({ tone: "warning", kind: "CVE scan failed", title: `${f.project} / ${f.name}`, detail: f.error?.split("\n")[0], href: f.href });
    }
    return rows.sort((a, b) => (a.tone === b.tone ? 0 : a.tone === "danger" ? -1 : 1));
  }, [summary.data, projects.data, uptime, data, backups, nodes.data, vulns]);

  const outliers = useMemo(() => findOutliers(data), [data]);
  const waste = useMemo(() => findWaste(data, publicServiceIds(summary.data ?? [])), [data, summary.data]);
  const changes = useMemo(() => recentChanges(data), [data]);
  const loadingAttention = summary.isPending || projects.isPending;

  return (
    <div className={cn("space-y-6 transition-opacity", dim && "opacity-60")}>
      <Section title="Needs attention" count={attention.length}>
        {loadingAttention ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-5 w-2/3" />
            <Skeleton className="h-5 w-1/2" />
          </div>
        ) : attention.length === 0 ? (
          <Empty>Nothing needs you: every service, check, build, certificate{isAdmin ? ", backup and server" : ""} is fine.</Empty>
        ) : (
          <ul>
            {attention.map((r, i) => (
              <li key={i} className="border-b border-[var(--border-subtle)] last:border-0">
                <AttentionLink row={r} />
              </li>
            ))}
          </ul>
        )}
      </Section>

      <div className="grid gap-6 lg:grid-cols-2">
        <Section title="Outliers" count={outliers.length}>
          {outliers.length === 0 ? (
            <Empty>No service is behaving unlike its last {data.range}.</Empty>
          ) : (
            <ul>
              {outliers.map((o, i) => (
                <li key={i} className="border-b border-[var(--border-subtle)] last:border-0">
                  <Link href={compareHref(o.id, o.metric)} className={cn(rowLink, "grid-cols-[1rem_minmax(0,1fr)]")}>
                    <ToneIcon tone={o.tone} />
                    <span className="min-w-0">
                      <span className="block truncate text-[var(--text-primary)]">
                        {o.label} <span className="text-[var(--text-tertiary)]">· {o.kind}</span>
                      </span>
                      <span className="block truncate text-xs text-[var(--text-tertiary)]">{o.detail}</span>
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </Section>

        <Section title="Waste" count={waste.length}>
          {waste.length === 0 ? (
            <Empty>Nothing idle or reserving far more than it uses.</Empty>
          ) : (
            <ul>
              {waste.map((w, i) => (
                <li key={i} className="border-b border-[var(--border-subtle)] last:border-0">
                  <Link href={compareHref(w.id, w.kind === "CPU reserved" ? "cpu" : w.kind === "Idle" ? "requests" : "memory")} className={cn(rowLink, "grid-cols-[minmax(0,1fr)]")}>
                    <span className="min-w-0">
                      <span className="block truncate text-[var(--text-primary)]">
                        {w.label} <span className="text-[var(--text-tertiary)]">· {w.kind}</span>
                      </span>
                      <span className="block truncate text-xs text-[var(--text-tertiary)]">{w.detail}</span>
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </Section>
      </div>

      <SecurityTable vulns={vulns} lastDeploy={lastDeployByProject(summary.data ?? [], projects.data ?? [])} />

      <Section title="Recent changes" count={changes.length}>
        {changes.length === 0 ? (
          <p className="px-4 py-4 text-[13px] text-[var(--text-tertiary)]">No deploys in the last {data.range}.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[40rem] text-[13px]">
              <thead>
                <tr className="border-b border-[var(--border-subtle)] font-mono text-[11px] uppercase tracking-wide text-[var(--text-tertiary)]">
                  <th className="px-4 py-2.5 text-left font-medium">When</th>
                  <th className="px-4 py-2.5 text-left font-medium">Deploy</th>
                  {(["memory", "p95", "errorRate"] as MetricKey[]).map((m) => (
                    <th key={m} className="px-4 py-2.5 text-right font-medium">
                      {metricDef(m).label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {changes.map((c) => (
                  <tr key={`${c.project}-${c.commitSha}-${c.at}`} className="border-b border-[var(--border-subtle)] last:border-0">
                    <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-[var(--text-tertiary)]">{relativeTime(new Date(c.at * 1000).toISOString())}</td>
                    <td className="max-w-0 px-4 py-2.5">
                      <Link
                        href={c.ids.length ? `/insights?v=compare&m=memory&s=${encodeURIComponent(c.ids.slice(0, 5).join(","))}` : `/projects/${encodeURIComponent(c.project)}`}
                        className="block truncate text-[var(--text-primary)] hover:underline"
                      >
                        {c.project}
                        <span className="text-[var(--text-tertiary)]"> / {c.services.join(", ")}</span>
                      </Link>
                      {c.commitMessage && <span className="block truncate text-xs text-[var(--text-tertiary)]">{c.commitMessage}</span>}
                    </td>
                    {(["memory", "p95", "errorRate"] as MetricKey[]).map((m) => (
                      <td key={m} className="whitespace-nowrap px-4 py-2.5 text-right">
                        <EffectBadge pct={c.effects[m]} />
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>
    </div>
  );
}

function AttentionLink({ row }: { row: AttentionRow }) {
  const body = (
    <>
      <ToneIcon tone={row.tone} />
      <span className="whitespace-nowrap text-xs font-medium text-[var(--text-secondary)]">{row.kind}</span>
      <span className="min-w-0">
        <span className="block truncate text-[var(--text-primary)]">{row.title}</span>
        {row.detail && <span className="block truncate text-xs text-[var(--text-tertiary)]">{row.detail}</span>}
      </span>
    </>
  );
  const cls = cn(rowLink, "grid-cols-[1rem_7.5rem_minmax(0,1fr)]");
  return row.href ? (
    <Link href={row.href} className={cls}>
      {body}
    </Link>
  ) : (
    <div className={cls}>{body}</div>
  );
}

// Higher is worse for every metric here, so a rise reads red.
function EffectBadge({ pct }: { pct: number | undefined }) {
  if (pct === undefined) return <span className="text-[var(--text-placeholder)]">—</span>;
  if (Math.abs(pct) < 5) return <span className="text-xs text-[var(--text-tertiary)]">≈ same</span>;
  const up = pct > 0;
  return (
    <span className={cn("inline-flex rounded-full px-2 py-px text-xs", up ? "bg-[var(--danger-subtle)] text-[var(--danger)]" : "bg-[var(--success-subtle)] text-[var(--success)]")}>
      {up ? "▲" : "▼"} {Math.min(999, Math.abs(pct)).toFixed(0)}%
    </span>
  );
}

function lastDeployByProject(summary: readonly ProjectSummaryItem[], projects: Parameters<typeof buildCardModel>[0][]): Map<string, string | undefined> {
  const byName = new Map(summary.map((i) => [i.project.metadata.name, i]));
  return new Map(projects.map((p) => [p.metadata.name, buildCardModel(p, byName.get(p.metadata.name)).lastDeployAt]));
}

const STALE_MS = 30 * 24 * 3600 * 1000;

function SecurityTable({ vulns, lastDeploy }: { vulns: FleetVulns | undefined; lastDeploy: Map<string, string | undefined> }) {
  const all = useMemo(
    () =>
      [...(vulns?.byProject ?? new Map()).entries()]
        .map(([project, v]) => ({ project, ...v }))
        .sort((a, b) => b.critical - a.critical || b.fixable - a.fixable || b.otherCritical - a.otherCritical || a.project.localeCompare(b.project)),
    [vulns],
  );
  // Projects with nothing critical or high anywhere fold into one line.
  const rows = all.filter((r) => r.critical + r.high + r.otherCritical + r.otherHigh > 0);
  const clean = all.length - rows.length;
  return (
    <Section title="Security by project" count={rows.filter((r) => r.critical > 0).length}>
      {!vulns ? (
        <div className="space-y-2 p-4">
          <Skeleton className="h-5 w-full" />
          <Skeleton className="h-5 w-full" />
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[44rem] text-[13px]">
            <thead>
              <tr className="border-b border-[var(--border-subtle)] font-mono text-[11px] uppercase tracking-wide text-[var(--text-tertiary)]">
                <th className="px-4 py-2.5 text-left font-medium">Project</th>
                <th className="px-4 py-2.5 text-right font-medium">Critical</th>
                <th className="px-4 py-2.5 text-right font-medium">High</th>
                <th className="px-4 py-2.5 text-right font-medium">Fixable</th>
                <th className="px-4 py-2.5 text-right font-medium">Staging & addons</th>
                <th className="px-4 py-2.5 text-right font-medium">Scanned</th>
                <th className="px-4 py-2.5 text-right font-medium">Last deploy</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const deployed = lastDeploy.get(r.project);
                const stale = deployed ? Date.now() - Date.parse(deployed) > STALE_MS : false;
                return (
                  <tr key={r.project} className="border-b border-[var(--border-subtle)] last:border-0">
                    <td className="px-4 py-2.5">
                      <Link href={r.worst?.href ?? `/projects/${encodeURIComponent(r.project)}`} className="text-[var(--text-primary)] hover:underline">
                        {r.project}
                      </Link>
                      {r.worst && r.worst.label !== r.project && <span className="text-xs text-[var(--text-tertiary)]"> · worst {r.worst.label}</span>}
                    </td>
                    <td className={cn("px-4 py-2.5 text-right font-mono", r.critical > 0 ? "text-[var(--danger)]" : "text-[var(--text-tertiary)]")}>{r.critical}</td>
                    <td className="px-4 py-2.5 text-right font-mono text-[var(--text-primary)]">{r.high}</td>
                    <td className="px-4 py-2.5 text-right font-mono text-[var(--text-primary)]">{r.fixable}</td>
                    <td className="px-4 py-2.5 text-right font-mono text-[var(--text-tertiary)]">
                      {r.otherCritical + r.otherHigh > 0 ? `${r.otherCritical} / ${r.otherHigh}` : "—"}
                    </td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right text-xs text-[var(--text-tertiary)]">{r.oldestScan ? relativeTime(r.oldestScan) : "—"}</td>
                    <td className={cn("whitespace-nowrap px-4 py-2.5 text-right text-xs", stale ? "text-[var(--warning)]" : "text-[var(--text-tertiary)]")}>
                      {deployed ? relativeTime(deployed) : "—"}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {clean > 0 && (
            <p className="border-t border-[var(--border-subtle)] px-4 py-2.5 text-xs text-[var(--text-tertiary)]">
              {clean} more {clean === 1 ? "project has" : "projects have"} no critical or high CVEs.
            </p>
          )}
        </div>
      )}
    </Section>
  );
}
