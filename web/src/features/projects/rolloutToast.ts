import type { QueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { envGroupName } from "@/lib/env-group";
import type { KusoEnvironment } from "@/types/projects";
import { listEnvironments } from "./api";

// A rollout's target: one env of the service, as it was when the deploy
// was sent. The completion stamp moves once per new ReplicaSet, so a
// changed stamp is the rollout landing, whatever the clocks say.
export interface RolloutBaseline {
  name: string;
  completedAt?: string;
  state?: string;
}

export type RolloutOutcome =
  | { kind: "pending"; sawRolling: boolean }
  | { kind: "done" }
  | { kind: "failed"; detail?: string };

const FAILED = new Set(["rollout_failing", "crashlooping", "stuck"]);
// Nothing rolls at zero replicas; the change applies on the next start.
const IDLE = new Set(["sleeping", "stopped"]);

// rolloutOutcome folds the target envs into one outcome. A failure the
// env was already in before the deploy only counts once a new rollout
// has been seen (or it's been long enough for one to have started), so
// fixing a failing health check isn't reported failed on the first poll.
export function rolloutOutcome(
  baseline: readonly RolloutBaseline[],
  envs: readonly KusoEnvironment[],
  opts: { sawRolling: boolean; elapsedMs: number },
): RolloutOutcome {
  let sawRolling = opts.sawRolling;
  let pending = false;
  for (const b of baseline) {
    const env = envs.find((e) => e.metadata.name === b.name);
    if (!env) continue;
    const state = env.status?.state;
    const completed = env.status?.rolloutCompletedAt;
    if (state === "deploying") sawRolling = true;
    if (state && FAILED.has(state)) {
      const already = !!b.state && FAILED.has(b.state);
      if (!already || sawRolling || opts.elapsedMs > 120_000) {
        return { kind: "failed", detail: env.status?.stateDetail || undefined };
      }
      pending = true;
      continue;
    }
    if (state && IDLE.has(state)) continue;
    const moved = !!completed && completed !== b.completedAt;
    // Old servers send no completion stamp: a rollout seen and settled.
    const settled = moved || (!completed && sawRolling && state === "running");
    if (!settled || state === "deploying") pending = true;
  }
  return pending ? { kind: "pending", sawRolling } : { kind: "done" };
}

function targetsOf(envs: readonly KusoEnvironment[], project: string, service: string, group?: string): RolloutBaseline[] {
  const fqn = `${project}-${service}`;
  return envs
    .filter((e) => e.spec.service === fqn && (!group || envGroupName(e) === group))
    .map((e) => ({ name: e.metadata.name, completedAt: e.status?.rolloutCompletedAt, state: e.status?.state }));
}

const POLL_MS = 3_000;
const GIVE_UP_MS = 15 * 60_000;
const running = new Map<string, () => void>();

export interface RolloutBaselineSnapshot {
  project: string;
  service: string;
  group?: string;
  targets: RolloutBaseline[];
}

// snapshotRollout records the target envs before the deploy request goes
// out. Call it first, then trackRollout once the request succeeded.
export async function snapshotRollout(project: string, service: string, group?: string): Promise<RolloutBaselineSnapshot> {
  try {
    return { project, service, group, targets: targetsOf(await listEnvironments(project), project, service, group) };
  } catch {
    return { project, service, group, targets: [] };
  }
}

// trackRollout shows one toast for a deploy: "Rolling out…" while the new
// pods come up, "Deployed" only once they're ready, or the failure. It
// polls the env list the dashboard already reads.
export function trackRollout(
  snap: RolloutBaselineSnapshot,
  opts: { label: string; qc?: QueryClient; startedText?: string; rollingText?: string; doneText?: string },
) {
  const key = `${snap.project}/${snap.service}/${snap.group ?? "*"}`;
  running.get(key)?.();
  const id = `rollout:${key}`;
  if (snap.targets.length === 0) {
    toast.success(opts.startedText ?? `Deploying ${opts.label}`, { id });
    return;
  }
  toast.loading(opts.rollingText ?? `Rolling out ${opts.label}…`, {
    id,
    description: "The current version keeps serving until the new pods are ready.",
  });
  const started = Date.now();
  let sawRolling = false;
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const stop = () => {
    stopped = true;
    if (timer) clearTimeout(timer);
    running.delete(key);
  };
  running.set(key, stop);
  const refresh = () => {
    void opts.qc?.invalidateQueries({ queryKey: ["projects", snap.project, "envs"] });
    void opts.qc?.invalidateQueries({ queryKey: ["projects", snap.project], exact: true });
    void opts.qc?.invalidateQueries({ queryKey: ["pods", "service", snap.project, snap.service] });
    void opts.qc?.invalidateQueries({ queryKey: ["projects", snap.project, "services", snap.service, "drift"] });
  };
  const tick = async () => {
    if (stopped) return;
    try {
      const envs = await listEnvironments(snap.project);
      const out = rolloutOutcome(snap.targets, envs, { sawRolling, elapsedMs: Date.now() - started });
      if (out.kind === "done") {
        stop();
        refresh();
        toast.success(opts.doneText ?? `Deployed ${opts.label}`, { id, description: "The new pods are ready and serving." });
        return;
      }
      if (out.kind === "failed") {
        stop();
        refresh();
        toast.error(`Rollout of ${opts.label} is failing`, {
          id,
          description: out.detail
            ? `${out.detail.charAt(0).toUpperCase()}${out.detail.slice(1)}.`
            : "Open the service for the cause.",
        });
        return;
      }
      sawRolling = out.sawRolling;
    } catch {
      // A failed poll isn't a failed rollout; try again.
    }
    if (Date.now() - started > GIVE_UP_MS) {
      stop();
      toast.message(`${opts.label} is still rolling out`, { id, description: "Check the service's Deployments tab." });
      return;
    }
    timer = setTimeout(() => void tick(), POLL_MS);
  };
  timer = setTimeout(() => void tick(), POLL_MS);
}
