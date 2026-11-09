import { lastDeployedAt } from "@/features/projects/facts";
import type { ProjectErrorSummary } from "@/features/services/api";
import { envGroupName } from "@/lib/env-group";
import { SERVICE_STATUS, type ServiceStatus, type StatusTone } from "@/lib/status";
import type { ProjectSummaryItem } from "@/features/projects";
import type { ProjectCardModel } from "./projectCardModel";

// Every service in the fleet in one flat list, in board order, so the
// pulse strip can draw one cell per service.
export interface FleetCell {
  project: string;
  service: string;
  status: ServiceStatus;
  tone: StatusTone;
}

export function fleetCells(models: ReadonlyArray<ProjectCardModel>): FleetCell[] {
  return models.flatMap((m) =>
    m.services.map((s) => ({ project: m.name, service: s.name, status: s.status, tone: SERVICE_STATUS[s.status].tone })),
  );
}

export function projectErrorTotal(summary: ProjectErrorSummary | undefined): number | undefined {
  if (!summary) return undefined;
  return Object.values(summary.services).reduce((n, s) => n + s.occurrences, 0);
}

export interface AttentionItem {
  project: string;
  service: string;
  status: ServiceStatus;
  tone: StatusTone;
  detail?: string;
}

const ATTENTION_RANK: Partial<Record<StatusTone, number>> = { danger: 0, warning: 1, progress: 2 };

// Services that need a human (failing, stuck, degraded) or are mid-deploy,
// worst first. Settled states (live, sleeping, stopped) never appear.
export function attentionItems(models: ReadonlyArray<ProjectCardModel>): AttentionItem[] {
  const items: AttentionItem[] = [];
  for (const m of models) {
    for (const s of m.services) {
      const tone = SERVICE_STATUS[s.status].tone;
      if (ATTENTION_RANK[tone] === undefined) continue;
      items.push({ project: m.name, service: s.name, status: s.status, tone, detail: s.detail });
    }
  }
  return items.sort(
    (a, b) =>
      (ATTENTION_RANK[a.tone] ?? 9) - (ATTENTION_RANK[b.tone] ?? 9) ||
      a.project.localeCompare(b.project) ||
      a.service.localeCompare(b.service),
  );
}

export interface RecentDeploy {
  project: string;
  service: string;
  env: string;
  at: string;
}

// The newest deploys across every project and env-group (staging too:
// "what changed lately" includes it), from the summary already loaded.
export function recentDeploys(items: ReadonlyArray<ProjectSummaryItem>, limit: number): RecentDeploy[] {
  const out: RecentDeploy[] = [];
  for (const item of items) {
    for (const env of item.environments) {
      const at = lastDeployedAt(env);
      if (!at) continue;
      const svc = item.services.find((s) => s.metadata.name === env.spec.service);
      const short = svc?.metadata.labels?.["kuso.sislelabs.com/service"] ?? env.spec.service ?? env.metadata.name;
      out.push({ project: item.project.metadata.name, service: short, env: envGroupName(env), at });
    }
  }
  return out.sort((a, b) => Date.parse(b.at) - Date.parse(a.at)).slice(0, limit);
}

export interface FootprintSlice {
  project: string;
  memBytes: number;
  share: number;
}

// The biggest memory consumers, each with its share of the fleet total;
// the long tail folds into one "other" slice so the bar stays readable.
export function memoryFootprint(
  models: ReadonlyArray<ProjectCardModel>,
  top: number,
): { total: number; cpuMillicores: number; slices: FootprintSlice[]; other: number } {
  const withMem = models.filter((m) => (m.metrics?.memBytes ?? 0) > 0);
  const total = withMem.reduce((n, m) => n + (m.metrics?.memBytes ?? 0), 0);
  const cpuMillicores = models.reduce((n, m) => n + (m.metrics?.cpuMillicores ?? 0), 0);
  if (total === 0) return { total: 0, cpuMillicores, slices: [], other: 0 };
  const sorted = [...withMem].sort((a, b) => (b.metrics?.memBytes ?? 0) - (a.metrics?.memBytes ?? 0));
  const slices = sorted.slice(0, top).map((m) => {
    const memBytes = m.metrics?.memBytes ?? 0;
    return { project: m.name, memBytes, share: memBytes / total };
  });
  const shown = slices.reduce((n, s) => n + s.memBytes, 0);
  return { total, cpuMillicores, slices, other: (total - shown) / total };
}

// headline is the one sentence under the page title: the state of the
// whole fleet, problems first.
export function headline(input: {
  projects: number;
  services: number;
  attention: ReadonlyArray<AttentionItem>;
  errors: number | undefined;
}): string {
  const { projects, services, attention, errors } = input;
  const broken = attention.filter((a) => a.tone === "danger" || a.tone === "warning").length;
  const moving = attention.filter((a) => a.tone === "progress").length;
  const parts = [`${projects} project${projects === 1 ? "" : "s"}`, `${services} service${services === 1 ? "" : "s"}`];
  if (broken > 0) parts.push(`${broken} need${broken === 1 ? "s" : ""} attention`);
  else parts.push("nothing failing");
  if (moving > 0) parts.push(`${moving} deploying`);
  if (errors !== undefined && errors > 0) parts.push(`${errors.toLocaleString()} error${errors === 1 ? "" : "s"} in 24h`);
  return parts.join(" · ");
}
