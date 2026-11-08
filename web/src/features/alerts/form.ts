import type { AlertKind, AlertRule, CreateAlertBody } from "./api";

// What the settings form shows for each kind. `threshold` stores into
// thresholdFloat (percent / ms) or thresholdInt (matches / days) —
// the server's per-kind contract, see server-go normalizeAlertBody.
interface KindMeta {
  label: string;
  group: "Service" | "Edge" | "Logs" | "Nodes";
  threshold?: { label: string; field: "thresholdFloat" | "thresholdInt"; placeholder: string };
  minRequests: boolean;
  window: boolean;
  scope: "none" | "project" | "env";
  help: string;
}

export const ALERT_KINDS: Record<AlertKind, KindMeta> = {
  http_5xx_rate: {
    label: "HTTP 5xx rate",
    group: "Service",
    threshold: { label: "Threshold (% 5xx)", field: "thresholdFloat", placeholder: "5" },
    minRequests: true,
    window: true,
    scope: "env",
    help: "Share of requests answered with a 5xx, per env, from traefik metrics.",
  },
  http_p95_latency: {
    label: "HTTP p95 latency",
    group: "Service",
    threshold: { label: "Threshold (ms)", field: "thresholdFloat", placeholder: "1000" },
    minRequests: true,
    window: true,
    scope: "env",
    help: "95th-percentile response time per env, from traefik metrics.",
  },
  cert_expiry: {
    label: "TLS certificate",
    group: "Edge",
    threshold: { label: "Days before expiry", field: "thresholdInt", placeholder: "14" },
    minRequests: false,
    window: false,
    scope: "env",
    help: "Fires when a cert expires within N days or its cert-manager Certificate isn't Ready.",
  },
  dns_mismatch: {
    label: "DNS mismatch",
    group: "Edge",
    minRequests: false,
    window: false,
    scope: "env",
    help: "Fires when an env hostname doesn't resolve to the cluster's ingress IPs. Cloudflare-proxied hosts are skipped.",
  },
  log_match: {
    label: "Log match",
    group: "Logs",
    threshold: { label: "Threshold (matches)", field: "thresholdInt", placeholder: "1" },
    minRequests: false,
    window: true,
    scope: "project",
    help: "Counts log lines containing the query.",
  },
  node_cpu: {
    label: "Node CPU",
    group: "Nodes",
    threshold: { label: "Threshold (%)", field: "thresholdFloat", placeholder: "80" },
    minRequests: false,
    window: true,
    scope: "none",
    help: "Any node's CPU above the threshold.",
  },
  node_mem: {
    label: "Node memory",
    group: "Nodes",
    threshold: { label: "Threshold (%)", field: "thresholdFloat", placeholder: "80" },
    minRequests: false,
    window: true,
    scope: "none",
    help: "Any node's memory above the threshold.",
  },
  node_disk: {
    label: "Node disk",
    group: "Nodes",
    threshold: { label: "Threshold (%)", field: "thresholdFloat", placeholder: "85" },
    minRequests: false,
    window: true,
    scope: "none",
    help: "Any node's disk usage above the threshold.",
  },
};

export interface RuleFormState {
  kind: AlertKind;
  name: string;
  project: string;
  service: string;
  env: string;
  query: string;
  threshold: string;
  minRequests: string;
  window: string;
  throttle: string;
  severity: AlertRule["severity"];
}

export function emptyRuleForm(kind: AlertKind): RuleFormState {
  return {
    kind,
    name: "",
    project: "",
    service: "",
    env: "",
    query: "",
    threshold: ALERT_KINDS[kind].threshold?.placeholder ?? "",
    minRequests: ALERT_KINDS[kind].minRequests ? "20" : "",
    window: "5m",
    throttle: "10m",
    severity: "warn",
  };
}

export type BuildResult = { ok: true; body: CreateAlertBody } | { ok: false; error: string };

// projectRequired: non-admin callers may only add rules scoped to a
// project they edit (server-go alerts.go), so an unscoped rule is
// invalid for them rather than a 403 after submit.
export function buildCreateBody(s: RuleFormState, opts: { projectRequired?: boolean } = {}): BuildResult {
  const meta = ALERT_KINDS[s.kind];
  if (opts.projectRequired && meta.scope === "none") {
    return { ok: false, error: "Node rules need an instance admin" };
  }
  const name = s.name.trim();
  if (!name) return { ok: false, error: "Name is required" };
  const body: CreateAlertBody = {
    name,
    kind: s.kind,
    severity: s.severity,
    windowSeconds: meta.window ? parseDur(s.window) : undefined,
    throttleSeconds: parseDur(s.throttle),
  };
  if (meta.scope !== "none") {
    const project = s.project.trim();
    const service = s.service.trim();
    const env = meta.scope === "env" ? s.env.trim() : "";
    if (opts.projectRequired && !project) {
      return { ok: false, error: "Pick a project" };
    }
    if ((service || env) && !project && meta.scope === "env") {
      return { ok: false, error: "Service and env scoping need a project" };
    }
    body.project = project || undefined;
    body.service = service || undefined;
    body.env = env || undefined;
  }
  if (s.kind === "log_match") {
    const query = s.query.trim();
    if (!query) return { ok: false, error: "Query is required" };
    body.query = query;
  }
  if (meta.threshold && s.threshold.trim() !== "") {
    const n = Number(s.threshold);
    if (!Number.isFinite(n)) return { ok: false, error: `${meta.threshold.label} must be a number` };
    if (meta.threshold.field === "thresholdInt") {
      if (!Number.isInteger(n)) return { ok: false, error: `${meta.threshold.label} must be a whole number` };
      body.thresholdInt = n;
    } else {
      body.thresholdFloat = n;
    }
  }
  if (meta.minRequests && s.minRequests.trim() !== "") {
    const n = Number(s.minRequests);
    if (!Number.isInteger(n) || n < 0) return { ok: false, error: "Min requests must be a whole number" };
    body.thresholdInt = n;
  }
  return { ok: true, body };
}

// alertKindLabel names a rule kind for people; kinds this UI doesn't
// know yet read as words instead of an identifier.
export function alertKindLabel(kind: string): string {
  const meta = (ALERT_KINDS as Record<string, KindMeta | undefined>)[kind];
  if (meta) return meta.label;
  const words = kind.replace(/[._-]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function describeRule(rule: AlertRule): string {
  const win = formatSec(rule.windowSeconds);
  switch (rule.kind) {
    case "log_match":
      return `"${rule.query ?? ""}" in the logs ≥ ${rule.thresholdInt ?? 1} times in ${win}`;
    case "http_5xx_rate":
      return `5xx ≥ ${rule.thresholdFloat ?? 5}% over ${win} (min ${rule.thresholdInt ?? 20} req)`;
    case "http_p95_latency":
      return `p95 ≥ ${rule.thresholdFloat ?? 1000} ms over ${win} (min ${rule.thresholdInt ?? 20} req)`;
    case "cert_expiry":
      return `expires within ${rule.thresholdInt ?? 14}d or not Ready`;
    case "dns_mismatch":
      return "host doesn't resolve to the cluster";
    default:
      return `≥ ${rule.thresholdFloat ?? 80}% (window ${win})`;
  }
}

// parseDur: "5m" / "30s" / "300" → seconds. Invalid → 0 (server applies default).
export function parseDur(s: string): number {
  const m = s.trim().match(/^(\d+)\s*([smhd]?)$/);
  if (!m) return 0;
  const n = parseInt(m[1], 10);
  switch (m[2]) {
    case "":
    case "s":
      return n;
    case "m":
      return n * 60;
    case "h":
      return n * 3600;
    case "d":
      return n * 86_400;
  }
  return 0;
}

export function formatSec(n: number): string {
  if (n >= 86_400) return `${Math.round(n / 86_400)}d`;
  if (n >= 3600) return `${Math.round(n / 3600)}h`;
  if (n >= 60) return `${Math.round(n / 60)}m`;
  return `${n}s`;
}

// Aliases callers may reasonably use in a prefill link; mapped onto the
// real kinds so "Alert me when errors spike" links don't silently fall
// back to the default.
const KIND_ALIASES: Record<string, AlertKind> = {
  error_rate: "http_5xx_rate",
  errors: "http_5xx_rate",
  "5xx": "http_5xx_rate",
  latency: "http_p95_latency",
  p95: "http_p95_latency",
};

export function parseAlertKind(raw: string | null): AlertKind | null {
  if (!raw) return null;
  const k = raw.trim().toLowerCase();
  if (k in ALERT_KINDS) return k as AlertKind;
  return KIND_ALIASES[k] ?? null;
}

// prefillRuleForm reads the /settings/alerts?new=1 deep-link contract:
// new=1 opens the create form; kind, project, service, env prefill it.
// Unknown kinds fall back to http_5xx_rate; scope fields the kind
// doesn't use are dropped. Returns null when new isn't set.
export function prefillRuleForm(params: URLSearchParams): RuleFormState | null {
  if (params.get("new") !== "1") return null;
  const kind = parseAlertKind(params.get("kind")) ?? "http_5xx_rate";
  const meta = ALERT_KINDS[kind];
  const f = emptyRuleForm(kind);
  if (meta.scope !== "none") {
    f.project = params.get("project")?.trim() ?? "";
    f.service = f.project ? (params.get("service")?.trim() ?? "") : "";
    if (meta.scope === "env" && f.project) f.env = params.get("env")?.trim() ?? "";
  }
  // log_match: the text to look for, e.g. from an error group.
  if (kind === "log_match") f.query = params.get("query")?.trim() ?? "";
  const target = f.service || f.project;
  if (target) f.name = `${target} ${meta.label}`;
  return f;
}
