import type { ProjectErrorSummary } from "@/features/services/api";
import { SERVICE_STATUS, type ServiceStatus, type StatusTone } from "@/lib/status";
import type { ProjectCardModel } from "./projectCardModel";

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


// wallTitle is the home page's one-line verdict on the whole fleet:
// problems win, then work in progress, then the all-clear.
export function wallTitle(input: {
  attention: ReadonlyArray<AttentionItem>;
  running: number;
}): string {
  const broken = input.attention.filter((a) => a.tone === "danger" || a.tone === "warning").length;
  if (broken > 0) return `${broken} ${broken === 1 ? "service needs" : "services need"} you`;
  const moving = input.attention.filter((a) => a.tone === "progress").length;
  if (input.running === 0) return "Nothing is running";
  if (moving > 0) return `Everything is up, ${moving} ${moving === 1 ? "deploy" : "deploys"} in progress`;
  return "Everything is up";
}

// lastDeployedProject is the production deploy the wall mentions: one
// project, not a feed of every env that rolled with it.
export function lastDeployedProject(
  models: ReadonlyArray<Pick<ProjectCardModel, "name" | "lastDeployAt">>,
): { name: string; at: string } | undefined {
  let best: { name: string; at: string } | undefined;
  for (const m of models) {
    if (!m.lastDeployAt) continue;
    if (!best || Date.parse(m.lastDeployAt) > Date.parse(best.at)) best = { name: m.name, at: m.lastDeployAt };
  }
  return best;
}
