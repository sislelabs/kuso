// Drag-to-connect helpers for the project canvas. Translates a React
// Flow Connection (source/target node IDs) into an env-var addition on
// the target service. Decoupled from ProjectCanvas so the logic is
// testable + the canvas component stays focused on layout/render.
//
// Node ID convention (set by initialNodes in ProjectCanvas):
//   svc:<fqn>     — KusoService node
//   addon:<fqn>   — KusoAddon node
//
// Var-name conventions:
//   service → service: `<UPPER_SOURCE_SHORT>_URL`, value
//                       `${{ <source-short>.URL }}` (in-cluster URL).
//                       Hyphens in the short name become underscores;
//                       leading digits are prefixed with `KUSO_` so
//                       the resulting name is a valid POSIX env name.
//   addon (postgres) → service: `DATABASE_URL = ${{ <addon>.DATABASE_URL }}`
//   addon (redis)    → service: `REDIS_URL    = ${{ <addon>.REDIS_URL }}`
//   addon (other)    → service: `<KIND>_URL   = ${{ <addon>.URL }}` (best-
//                       effort default; the user can rename afterwards)
import type { KusoAddon, KusoService } from "@/types/projects";

export interface ConnectInputs {
  project: string;
  sourceId: string;
  targetId: string;
  services: KusoService[];
  addons: KusoAddon[];
}

export interface ConnectionPlan {
  // FQ name of the target service whose env we'll mutate.
  targetService: string;
  // Env-var name + value to ADD (or skip if already present with the
  // same value). Caller appends to the existing envVars and POSTs.
  varName: string;
  varValue: string;
  // Free-text reason used in the toast on success.
  summary: string;
}

export type ConnectResult =
  | { ok: true; plan: ConnectionPlan }
  | { ok: false; reason: string };

// shortName strips the leading "<project>-" prefix from a kuso CR
// name, mirroring the server's serviceShortName helper.
function shortName(project: string, fqn: string): string {
  const prefix = project + "-";
  return fqn.startsWith(prefix) ? fqn.slice(prefix.length) : fqn;
}

// envVarName converts an arbitrary string into a valid POSIX env-var
// name. Lowercase + hyphens get uppercased / replaced; a leading
// digit gets a KUSO_ prefix so the result always begins with [A-Z_].
function envVarName(short: string, suffix: string): string {
  let s = short.toUpperCase().replace(/[^A-Z0-9]/g, "_");
  if (/^[0-9]/.test(s)) s = "KUSO_" + s;
  return s + "_" + suffix;
}

// addonDefault picks the canonical (key, var-name) pair for a given
// addon kind. Mirrors what the helm chart for each kind stamps into
// the conn-secret. Falls back to URL/{KIND}_URL for unknown kinds —
// the user can rename afterwards.
function addonDefault(kind: string): { key: string; varName: string } {
  const k = (kind || "").toLowerCase();
  switch (k) {
    case "postgres":
    case "postgresql":
    case "mysql":
    case "mariadb":
      return { key: "DATABASE_URL", varName: "DATABASE_URL" };
    case "redis":
    case "valkey":
      return { key: "REDIS_URL", varName: "REDIS_URL" };
    case "mongodb":
    case "mongo":
      return { key: "MONGODB_URL", varName: "MONGODB_URL" };
    default: {
      const upper = k.replace(/[^a-z0-9]/g, "").toUpperCase() || "ADDON";
      return { key: "URL", varName: upper + "_URL" };
    }
  }
}

// planConnection turns a Connection (source + target node ID) into a
// ConnectionPlan describing the env-var addition. Returns ok=false
// with a human-readable reason for unsupported combinations (e.g.
// service → addon, addon → addon, self-loop).
export function planConnection(input: ConnectInputs): ConnectResult {
  const { project, sourceId, targetId, services, addons } = input;
  if (sourceId === targetId) {
    return { ok: false, reason: "Cannot connect a node to itself." };
  }
  const [srcKind, srcFqn] = sourceId.split(":");
  const [tgtKind, tgtFqn] = targetId.split(":");
  if (!srcKind || !srcFqn || !tgtKind || !tgtFqn) {
    return { ok: false, reason: "Invalid node IDs." };
  }
  // Target must always be a service — addons aren't configured by
  // their consumers.
  if (tgtKind !== "svc") {
    return { ok: false, reason: "Drop the connection on a service, not an addon." };
  }
  const targetSvc = services.find((s) => s.metadata.name === tgtFqn);
  if (!targetSvc) {
    return { ok: false, reason: `Target service ${tgtFqn} not found.` };
  }

  if (srcKind === "svc") {
    const sourceSvc = services.find((s) => s.metadata.name === srcFqn);
    if (!sourceSvc) {
      return { ok: false, reason: `Source service ${srcFqn} not found.` };
    }
    const srcShort = shortName(project, srcFqn);
    const tgtShort = shortName(project, tgtFqn);
    return {
      ok: true,
      plan: {
        targetService: tgtShort,
        varName: envVarName(srcShort, "URL"),
        varValue: `\${{ ${srcShort}.URL }}`,
        summary: `${srcShort} → ${tgtShort}`,
      },
    };
  }

  if (srcKind === "addon") {
    const sourceAddon = addons.find((a) => a.metadata.name === srcFqn);
    if (!sourceAddon) {
      return { ok: false, reason: `Source addon ${srcFqn} not found.` };
    }
    const addonShort = shortName(project, srcFqn);
    const tgtShort = shortName(project, tgtFqn);
    const def = addonDefault(sourceAddon.spec.kind);
    return {
      ok: true,
      plan: {
        targetService: tgtShort,
        varName: def.varName,
        varValue: `\${{ ${addonShort}.${def.key} }}`,
        summary: `${addonShort} → ${tgtShort}`,
      },
    };
  }

  return { ok: false, reason: `Unsupported source: ${srcKind}` };
}
