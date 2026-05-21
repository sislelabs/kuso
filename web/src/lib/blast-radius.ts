// blast-radius.ts encodes docs/EDIT_SAFETY.md as structured data so
// the UI can warn — at commit time — what a service/addon spec edit
// will actually do to the running workload.
//
// EDIT_SAFETY.md itself says: "the rows above with ⚠️ or ❌ should
// produce confirmation dialogs in the dashboard." This module is that
// table; DiffConfirmDialog renders the warnings.

export type BlastLevel = "info" | "warn" | "danger";

export interface BlastInfo {
  level: BlastLevel;
  // Short, user-facing sentence — what happens when this field saves.
  message: string;
}

// SERVICE_BLAST maps a PatchServiceBody field key to its blast radius.
// Keys match the field names ServiceSettingsPanel.onSave puts on the
// patch body (and the field labels config-as-code surfaces).
const SERVICE_BLAST: Record<string, BlastInfo> = {
  displayName: {
    level: "info",
    message: "Pure UI label — no redeploy, no reconcile.",
  },
  repo: {
    level: "info",
    message: "Only the next build uses the new repo; running pods are untouched.",
  },
  envVars: {
    level: "warn",
    message:
      "Re-renders the Deployment → rolling restart, ~10–30s of mixed-version traffic.",
  },
  envFromSecrets: {
    level: "warn",
    message: "Rolling restart of the pods as the env-from secret rev bumps.",
  },
  image: {
    level: "warn",
    message: "Image pull + rolling restart — the standard deploy path.",
  },
  replicaCount: {
    level: "info",
    message: "Pure scale change; no pod restart (ignored when autoscaling is on).",
  },
  scale: {
    level: "warn",
    message:
      "Re-renders the HPA — brief autoscaler gap. Setting min to 0 also enables sleep.",
  },
  sleep: {
    level: "info",
    message: "Affects idle behaviour only; pods wake on the next request.",
  },
  port: {
    level: "warn",
    message:
      "Service targetPort changes — ~5s where new connections fail until Traefik picks up the new endpoint.",
  },
  internal: {
    level: "warn",
    message:
      "Toggles the public Ingress. Going internal removes the public URL; going public re-creates the Ingress (~30s gap).",
  },
  domains: {
    level: "danger",
    message:
      "Ingress rewrite + cert-manager re-issues TLS. Let's Encrypt prod limits: 5 failed validations/hour, 50 certs/week per root domain — don't churn this.",
  },
  placement: {
    level: "warn",
    message:
      "nodeSelector/affinity change — pods are evicted from non-matching nodes and restarted elsewhere. Brief downtime if no matching node has capacity.",
  },
  volumes: {
    level: "danger",
    message:
      "Adding a volume redeploys. REMOVING a volume detaches its PVC and orphans the data — the PVC is not auto-deleted, but no pod mounts it.",
  },
  runtime: {
    level: "danger",
    message:
      "Changing the runtime rewrites the Deployment, Service, Ingress and probes — equivalent to recreating the environment. Prefer delete + recreate.",
  },
  command: {
    level: "warn",
    message: "Rolling restart with the new argv (worker runtime only).",
  },
  previews: {
    level: "info",
    message:
      "Affects only future PR opens; existing preview environments survive until their TTL.",
  },
};

// ADDON_BLAST maps an addon patch field to its blast radius. Most
// addon fields are immutable post-creation (StatefulSet PVC templates
// can't change in place) — those are marked danger so the UI can
// steer the user to backup → recreate → restore.
const ADDON_BLAST: Record<string, BlastInfo> = {
  placement: {
    level: "warn",
    message: "Addon pod is evicted and rescheduled — brief data-plane gap; clients reconnect.",
  },
  resources: {
    level: "warn",
    message: "Rolling restart of the addon pod.",
  },
  pooler: {
    level: "info",
    message: "Adds/removes the PgBouncer pooler Deployment; the database itself is untouched.",
  },
  backup: {
    level: "info",
    message: "Changes the backup schedule only; no effect on the running database.",
  },
  version: {
    level: "danger",
    message:
      "Engine version is immutable post-create. The only path is: backup → delete → create new → restore.",
  },
  size: {
    level: "danger",
    message: "Tier/size is immutable — the StatefulSet PVC template can't change in place.",
  },
  ha: {
    level: "danger",
    message: "The HA toggle is immutable post-create — recreate the addon to change it.",
  },
  storageSize: {
    level: "danger",
    message: "Storage size is immutable — the PVC template can't grow in place.",
  },
  database: {
    level: "danger",
    message: "Changing the database name orphans the existing data.",
  },
};

// rank orders levels so a set of changes can report its worst case.
const rank: Record<BlastLevel, number> = { info: 0, warn: 1, danger: 2 };

// serviceBlast returns the blast info for one service field, or null
// when the field has no notable blast radius.
export function serviceBlast(field: string): BlastInfo | null {
  return SERVICE_BLAST[field] ?? null;
}

// addonBlast returns the blast info for one addon field.
export function addonBlast(field: string): BlastInfo | null {
  return ADDON_BLAST[field] ?? null;
}

// worstLevel reduces a set of blast infos to the highest severity
// present — drives the summary banner colour/wording.
export function worstLevel(infos: (BlastInfo | null)[]): BlastLevel {
  let worst: BlastLevel = "info";
  for (const i of infos) {
    if (i && rank[i.level] > rank[worst]) worst = i.level;
  }
  return worst;
}

// summaryFor returns a one-line headline for the confirm dialog given
// the worst blast level among the pending changes.
export function summaryFor(level: BlastLevel): string {
  switch (level) {
    case "danger":
      return "One or more changes are destructive or hit a rate limit — review carefully.";
    case "warn":
      return "Saving will roll the pods. The current pod stays up until the new one is Ready.";
    default:
      return "These changes apply without restarting the workload.";
  }
}
