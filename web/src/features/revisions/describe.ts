import type { Revision } from "./api";

export interface RevisionView {
  label: string;
  // Short detail after the label ("3 keys"); "" when there's nothing useful.
  detail: string;
  revertable: boolean;
}

interface SnapshotProbe {
  op: string;
  informational: boolean;
}

function probe(snapshot: unknown): SnapshotProbe | null {
  if (!snapshot || typeof snapshot !== "object") return null;
  const s = snapshot as Record<string, unknown>;
  return {
    op: typeof s.op === "string" ? s.op : "",
    informational: s.informational === true,
  };
}

const OP_LABEL: Record<string, string> = {
  "": "Settings changed",
  "service.envVars": "Env vars changed",
  "service.envSecret": "Secret changed",
  "service.sharedEnvKeys": "Shared vars changed",
  "service.subscribedAddons": "Addons changed",
  "service.domains": "Domains changed",
  "service.rename": "Renamed",
  "environment.create": "Environment created",
  "environment.domains": "Domains changed",
  "environment.overrides": "Env vars changed",
  "environment.branch": "Branch changed",
};

const ENV_PREFIX = /^env [^\s:]+: /;
const VERB = /^(env set|env unset|env share|env unshare|domain add|domain remove|addon subscribe|addon unsubscribe) /;

// summaryItems pulls the names out of a server summary such as
// "env staging: env set A, B; env unset C (secret)" → ["A", "B", "C"].
// Returns null when the summary isn't in the verb-list shape.
export function summaryItems(summary: string): string[] | null {
  const body = summary.replace(ENV_PREFIX, "");
  const names = new Set<string>();
  for (const part of body.split("; ")) {
    const m = VERB.exec(part);
    if (!m) return null;
    for (const n of part.slice(m[0].length).split(", ")) {
      const name = n.replace(/ \(secret\)$/, "").trim();
      if (name) names.add(name);
    }
  }
  return names.size > 0 ? [...names] : null;
}

function plural(n: number, one: string): string {
  return `${n} ${one}${n === 1 ? "" : "s"}`;
}

export function describeRevision(rev: Revision): RevisionView {
  const p = probe(rev.snapshot);
  const op = p?.op ?? "";
  const summary = rev.summary ?? "";
  const label = OP_LABEL[op] ?? "Config changed";
  const items = summaryItems(summary);
  let detail = "";
  if (items) {
    const noun = op.endsWith("domains") ? "domain" : op.endsWith("subscribedAddons") ? "addon" : "key";
    detail = plural(items.length, noun);
  } else if (summary && summary !== "patch") {
    detail = summary.replace(ENV_PREFIX, "");
  }
  // Unparseable snapshots are treated as informational server-side too.
  return { label, detail, revertable: p !== null && !p.informational };
}

// envRevisionName is the environment-kind revision name: the env CR
// name without the project prefix ("web-staging").
export function envRevisionName(project: string, envCRName: string): string {
  const prefix = `${project}-`;
  return envCRName.startsWith(prefix) ? envCRName.slice(prefix.length) : envCRName;
}
