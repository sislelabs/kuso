import { ALERT_KINDS } from "./form";
import type { AlertKind, AlertRule } from "./api";

// Who may manage which alert rules — mirrors server-go alerts.go
// alertAccess. Instance admins manage everything. Members manage rules
// scoped to a project they can edit; rules with no project and node
// rules (scope "none") watch the whole cluster and stay admin-only.

type ProjectRole = "admin" | "editor" | "viewer";

export interface AlertAccessSession {
  adminAll: boolean;
  projectRoles: Record<string, ProjectRole>;
}

export function canEditAlertProject(s: AlertAccessSession, project: string): boolean {
  if (s.adminAll) return true;
  const role = s.projectRoles[project];
  return role === "admin" || role === "editor";
}

export function isClusterWideRule(rule: Pick<AlertRule, "project" | "kind">): boolean {
  return !rule.project || ALERT_KINDS[rule.kind]?.scope === "none";
}

export function canManageAlertRule(s: AlertAccessSession, rule: Pick<AlertRule, "project" | "kind">): boolean {
  if (s.adminAll) return true;
  if (isClusterWideRule(rule)) return false;
  return canEditAlertProject(s, rule.project ?? "");
}

// Kinds the caller may pick in the create form.
export function alertKindsFor(s: AlertAccessSession): AlertKind[] {
  const all = Object.keys(ALERT_KINDS) as AlertKind[];
  return s.adminAll ? all : all.filter((k) => ALERT_KINDS[k].scope !== "none");
}

// Projects the caller may scope a new rule to, from the names they can see.
export function editableAlertProjects(s: AlertAccessSession, projects: string[]): string[] {
  return projects.filter((p) => canEditAlertProject(s, p)).sort((a, b) => a.localeCompare(b));
}

// The text a member copies to an admin when this server keeps alert
// rules admin-only (servers older than project-scoped alert access).
export function alertRequestText(opts: { project?: string; service?: string; kind?: AlertKind }): string {
  const kind = ALERT_KINDS[opts.kind ?? "http_5xx_rate"].label;
  const target = opts.project ? `${opts.project}${opts.service ? `/${opts.service}` : ""}` : "<project>/<service>";
  return [
    `Could you add a kuso alert rule for ${target}?`,
    `Kind: ${kind}`,
    "Threshold / window: <what should page us>",
    "Settings → Alert rules → New rule.",
  ].join("\n");
}
