// Every env write rolls pods: CR literals re-render the Deployment and
// managed-secret writes bump spec.secretsRev (server-go
// projects.bumpSecretsRevForService). There is no "save without restart",
// so the editor states the cost instead of offering a fake choice.

export interface RestartImpact {
  pods: number;
  // env-group names with at least one ready pod.
  envs: string[];
}

interface EnvLike {
  spec: { service?: string };
  status?: Record<string, unknown>;
}

function readyPods(status: Record<string, unknown> | undefined): number {
  const replicas = status?.replicas;
  if (!replicas || typeof replicas !== "object") return 0;
  const ready = (replicas as Record<string, unknown>).ready;
  return typeof ready === "number" && ready > 0 ? ready : 0;
}

export function restartImpactFromEnvs<E extends EnvLike>(
  envs: readonly E[],
  project: string,
  service: string,
  groupName: (e: E) => string,
): RestartImpact {
  let pods = 0;
  const names: string[] = [];
  for (const e of envs) {
    if (e.spec.service !== service && e.spec.service !== `${project}-${service}`) continue;
    const n = readyPods(e.status);
    if (n === 0) continue;
    pods += n;
    names.push(groupName(e));
  }
  return { pods, envs: names.sort() };
}

export function restartNote(impact: RestartImpact | undefined): string {
  if (!impact) return "Saving restarts this service's pods so they pick up the new values.";
  if (impact.pods === 0) return "No pods are running right now. The new values apply on the next start.";
  const pods = impact.pods === 1 ? "1 running pod" : `${impact.pods} running pods`;
  const where = impact.envs.length ? ` (${impact.envs.join(", ")})` : "";
  return `Saving restarts ${pods}${where}. Env vars can't be applied without a restart.`;
}
