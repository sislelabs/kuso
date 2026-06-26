import { api } from "@/lib/api-client";

// ReconcileIssue is one finding from the reconcile-health scan. The
// server walks every Kuso CR + its rendered helm release and reports
// anything that isn't cleanly reconciling — a failed helm install, a
// stuck addon, an orphaned env, etc.
//
// severity ∈ critical | warning | info.
//
//   safe + action  → there's a one-click remediation the server can
//                    apply on its own (POST /remediate).
//   !safe          → needs human judgment; we surface runbookCmd as a
//                    copy-paste instead of an auto-fix button.
export interface ReconcileIssue {
  resource: string;
  namespace?: string;
  project?: string;
  // type is a short machine code for the issue class (e.g.
  // "helm-failed", "orphaned-env"). kind/addonKind describe the CR.
  type?: string;
  addonKind?: string;
  kind?: string;
  severity: "critical" | "warning" | "info";
  summary: string;
  // detail carries the raw helm/operator error — rendered monospace.
  detail?: string;
  // action is the remediation verb the server understands; passed back
  // verbatim on the remediate POST.
  action?: string;
  safe?: boolean;
  // fix is prose explaining what the remediation will do.
  fix?: string;
  // runbookCmd is the copy-paste command for !safe issues.
  runbookCmd?: string;
}

export interface ReconcileReport {
  issues: ReconcileIssue[];
  healthy: number;
  scanned: number;
  critical: number;
  warning: number;
  info: number;
}

export interface RemediateRequest {
  resource: string;
  action?: string;
}

export interface RemediateResult {
  resource: string;
  action: string;
  applied: boolean;
  message: string;
}

export async function getReconcileHealth(): Promise<ReconcileReport> {
  return api("/api/health/reconcile");
}

export async function remediate(body: RemediateRequest): Promise<RemediateResult> {
  return api("/api/health/reconcile/remediate", { method: "POST", body });
}
