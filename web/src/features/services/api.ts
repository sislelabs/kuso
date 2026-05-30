import { api } from "@/lib/api-client";
import type { KusoEnvVar, KusoService } from "@/types/projects";

export async function getService(project: string, service: string): Promise<KusoService> {
  return api(`/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}`);
}

// masked=true means the caller isn't allowed to read env VALUES
// (role-system v2: values are admin-only). The server returns the keys
// with values replaced by a sentinel; the editor must render read-only
// so a non-admin can't accidentally save the sentinel back over real
// values.
export async function getServiceEnv(
  project: string,
  service: string
): Promise<{ envVars: KusoEnvVar[]; masked?: boolean }> {
  return api(`/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/env`);
}

export async function setServiceEnv(
  project: string,
  service: string,
  envVars: KusoEnvVar[]
): Promise<void> {
  // allowPending=true lets the user save a `${{ addon.KEY }}` ref
  // before the addon's connection Secret has been created. The pod
  // sits in CreateContainerConfigError until the secret materialises,
  // and the deployments tab shows it as "addon pending" instead of a
  // hard error on save. Mirrors how Vercel/Heroku let you wire env
  // vars to integrations that aren't fully provisioned yet.
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/env`,
    { method: "POST", body: { envVars, allowPending: true } }
  );
}

export interface DetectedEnv {
  // Names surfaced by the build-time env-detect init container
  // (.env.example + source grep). Empty until the next build runs.
  names: string[];
  // RFC3339 of the build that produced `names`. Empty when no
  // detection has been emitted yet (older build pod, never built).
  detectedAt?: string;
  // Runtime crash hints from the log shipper's missing-env regex
  // matcher. Each entry pins the var name + the log line that
  // triggered the match. Newest first.
  hints?: { project: string; service: string; name: string; lastLine: string; lastSeen: string }[];
}

export async function getDetectedEnv(project: string, service: string): Promise<DetectedEnv> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/env/detected`,
  );
}

// DriftReport mirrors projects.DriftReport on the server. All three
// arrays empty + rolloutPending=false means the running pods match
// the saved spec.
export interface DriftReport {
  // Service spec ↔ env CR: propagated fields out of sync. Should
  // always be empty in steady state.
  specPending: string[];
  // helm-operator hasn't observed the latest env CR generation yet
  // (brief window after a save).
  rolloutPending: boolean;
  // Env CR ↔ live Deployment pod template. Non-empty means the
  // user's edit reached the spec but kube hasn't rolled the new
  // pods yet — common after an env-var edit while a CrashLoop or
  // image-pull failure blocks the rollout. This is the surface
  // users actually feel.
  podsStale: string[];
  // RFC3339 timestamp of the youngest non-terminating pod. Used by
  // the editor to render a "Saved & rolled out Ns ago" banner that
  // survives a page refresh — without this, post-save feedback was
  // purely client-side state that was wiped on refresh.
  lastRolloutAt?: string;
  // RFC3339 timestamp of the last kuso-server spec write to the env CR.
  // Pairs with lastRolloutAt to render a single "Saved Ns ago — pod
  // started Ms after save" line in the env editor (replaces the
  // 3-state chip that used to flicker).
  lastSpecMutation?: string;
  envName?: string;
  // helm-operator's last release error (chart render failure, image
  // pull failure, helm release stuck in pending-upgrade, etc).
  // Empty when the last reconcile succeeded. Surface this in the
  // service overlay near the rolloutPending chip so users don't have
  // to kubectl-spelunk the operator pod logs to find out why their
  // edit didn't take.
  helmError?: string;
  // helm-operator phase: Deployed | Failed | Pending. The UI badges
  // anything other than Deployed.
  helmReleasePhase?: string;
}

export async function getDrift(project: string, service: string): Promise<DriftReport> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/drift`,
  );
}

export interface BuildSummary {
  id: string;
  serviceName: string;
  branch?: string;
  commitSha?: string;
  commitMessage?: string;
  imageTag?: string;
  // status ∈ queued | pending | running | succeeded | failed | cancelled
  status: string;
  startedAt?: string;
  finishedAt?: string;
  // Trigger context: who/what kicked off the build. Surfaces in the
  // deployments tab so users can answer "who broke prod" without git
  // archaeology. source ∈ user | webhook | api | system.
  triggeredBy?: string;
  triggeredByUser?: string;
  // errorMessage is the extracted failure cause for status=failed
  // builds. Server-side archiveLogs scans the build's tail logs +
  // the kubelet's terminated reason and stamps the hit. The UI
  // renders it as a sticky red banner above the log viewer.
  errorMessage?: string;
}

export async function listBuilds(project: string, service: string): Promise<BuildSummary[]> {
  return api(`/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/builds`);
}

// ErrorGroup is one fingerprint-grouped error from /api/projects/.../errors.
// firstSeen / lastSeen / count drive the row layout; sampleLine is the
// raw line shown in the drill-down.
export interface ErrorGroup {
  fingerprint: string;
  message: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
  sampleLine: string;
  sampleEnv?: string;
  samplePod?: string;
}

// listErrors fetches the current error groups for a service. `since`
// is a Go-flavoured duration string ("24h", "7d"); the server caps
// it at 30d.
export async function listErrors(
  project: string,
  service: string,
  since = "24h",
): Promise<ErrorGroup[]> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/errors?since=${encodeURIComponent(since)}`,
  );
}

// cancelBuild stops an in-flight build. 204 on success; 400 if the
// build is already in a terminal phase (succeeded/failed/cancelled);
// 404 for an unknown build id.
export async function cancelBuild(
  project: string,
  service: string,
  buildId: string,
): Promise<void> {
  await api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/builds/${encodeURIComponent(buildId)}/cancel`,
    { method: "POST" },
  );
}

export async function triggerBuild(
  project: string,
  service: string,
  body: { branch?: string; ref?: string } = {}
): Promise<BuildSummary> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/builds`,
    { method: "POST", body }
  );
}

export async function wakeService(project: string, service: string): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/wake`,
    { method: "POST" }
  );
}

export async function deleteService(project: string, service: string): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}`,
    { method: "DELETE" }
  );
}

// renameService is clone-then-delete on the server side. The new
// service comes up first, the old comes down second; expect a
// brief 503 window on the live URL during the swap.
export async function renameService(
  project: string,
  service: string,
  newName: string
): Promise<KusoService> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/rename`,
    { method: "POST", body: { newName } }
  );
}

export interface PatchServiceBody {
  displayName?: string;
  port?: number;
  // internal=true skips the public Ingress; service still has its
  // in-cluster Service so siblings can reach it via cluster DNS.
  internal?: boolean;
  runtime?: string;
  domains?: { host: string; tls?: boolean }[];
  scale?: { min?: number; max?: number; targetCPU?: number };
  sleep?: { enabled?: boolean; afterMinutes?: number };
  volumes?: VolumePatch[];
  placement?: PlacementPatch;
  repo?: PatchRepoBody;
  // Per-service preview opt-out. {disabled:true} skips PR previews
  // for this service even when the project toggle is on.
  // {clear:true} drops the override, falling back to project default.
  previews?: { disabled?: boolean; clear?: boolean };
}

export interface PatchRepoBody {
  url: string;
  branch?: string;
  path?: string;
  installationId?: number;
}

export interface PlacementPatch {
  labels?: Record<string, string>;
  nodes?: string[];
  // Server interprets clear=true as "drop the override, fall back
  // to project default". Distinct from sending an empty object,
  // which means "explicitly schedule anywhere".
  clear?: boolean;
}

export interface VolumePatch {
  name: string;
  mountPath: string;
  sizeGi?: number;
  storageClass?: string;
  accessMode?: string;
}

export async function patchService(
  project: string,
  service: string,
  body: PatchServiceBody
): Promise<KusoService> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}`,
    { method: "PATCH", body }
  );
}

export async function listAddonSecretKeys(project: string, addon: string): Promise<{ keys: string[] }> {
  return api(`/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/secret-keys`);
}

export async function getServiceLogs(
  project: string,
  service: string,
  env = "production",
  lines = 200
): Promise<{ project: string; service: string; env: string; lines: { pod: string; line: string }[] }> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/logs?env=${encodeURIComponent(env)}&lines=${lines}`
  );
}

// ---- Crons ----

export interface KusoCron {
  metadata: { name: string; namespace?: string; creationTimestamp?: string };
  spec: {
    project: string;
    service: string;
    schedule: string;
    command: string[];
    suspend?: boolean;
    concurrencyPolicy?: string;
    activeDeadlineSeconds?: number;
  };
  status?: Record<string, unknown>;
}

export interface CreateCronBody {
  name: string;
  schedule: string;
  command: string[];
  suspend?: boolean;
  concurrencyPolicy?: "Allow" | "Forbid" | "Replace";
  activeDeadlineSeconds?: number;
}

export async function listServiceCrons(project: string, service: string): Promise<KusoCron[]> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/crons`
  );
}

export async function addCron(project: string, service: string, body: CreateCronBody): Promise<KusoCron> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/crons`,
    { method: "POST", body }
  );
}

export async function deleteCron(project: string, service: string, name: string): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/crons/${encodeURIComponent(name)}`,
    { method: "DELETE" }
  );
}

export async function syncCron(project: string, service: string, name: string): Promise<KusoCron> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/crons/${encodeURIComponent(name)}/sync`,
    { method: "POST" }
  );
}

export async function rollbackBuild(
  project: string,
  service: string,
  build: string,
  env?: string,
): Promise<unknown> {
  // Scope rollback to the active env. Defaults to production server-
  // side when omitted; the overlay's caller passes the env-switcher
  // value so rolling back a staging deployment doesn't accidentally
  // re-point production at the staging image.
  const qs = env ? `?env=${encodeURIComponent(env)}` : "";
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/builds/${encodeURIComponent(build)}/rollback${qs}`,
    { method: "POST" }
  );
}

// ---- Log search ----

export interface LogLine {
  id: number;
  ts: string;
  pod: string;
  project: string;
  service: string;
  env: string;
  line: string;
}

export interface LogSearchResponse {
  project: string;
  service: string;
  q: string;
  lines: LogLine[];
}

export async function searchServiceLogs(
  project: string,
  service: string,
  params: { q?: string; env?: string; since?: string; until?: string; limit?: number }
): Promise<LogSearchResponse> {
  const sp = new URLSearchParams();
  if (params.q) sp.set("q", params.q);
  if (params.env) sp.set("env", params.env);
  if (params.since) sp.set("since", params.since);
  if (params.until) sp.set("until", params.until);
  if (params.limit) sp.set("limit", String(params.limit));
  const query = sp.toString();
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/logs/search${query ? "?" + query : ""}`
  );
}

// KusoRun — one-shot task pod bound to a service's most-recent
// succeeded build image. Surfaced in the Runs tab.
//
// The server returns the full KusoRun CR (metadata + spec). We type
// just the fields the UI renders + the run-phase annotation the
// poller stamps; the rest passes through unobserved so a server
// schema bump doesn't break the client.
export interface KusoRun {
  metadata: {
    name: string;
    namespace: string;
    creationTimestamp?: string;
    annotations?: Record<string, string>;
  };
  spec: {
    project: string;
    service: string;          // CR-name shape: <project>-<service>
    command: string[];
    env?: { name: string; value: string }[];
    timeoutSeconds?: number;
    triggeredBy?: string;     // "user" | "api" | "system"
    triggeredByUser?: string;
    done?: boolean;
    image?: { repository: string; tag: string; pullPolicy?: string };
  };
}

// runPhase plucks the phase annotation, defaulting to "pending" so
// fresh CRs render with the right badge before the poller fires.
export function runPhase(r: KusoRun): string {
  return r.metadata.annotations?.["kuso.sislelabs.com/run-phase"] ?? "pending";
}

// runMessage returns the failure reason the poller stamps on
// terminal failure. Empty otherwise.
export function runMessage(r: KusoRun): string {
  return r.metadata.annotations?.["kuso.sislelabs.com/run-message"] ?? "";
}

// runCompletedAt returns the RFC3339 completion time the poller
// stamps. Empty for in-flight runs.
export function runCompletedAt(r: KusoRun): string {
  return r.metadata.annotations?.["kuso.sislelabs.com/run-completed-at"] ?? "";
}

export interface CreateRunRequest {
  command: string[];
  env?: { name: string; value: string }[];
  timeoutSeconds?: number;
}

export async function listRuns(project: string, service: string): Promise<KusoRun[]> {
  return api(`/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/runs`);
}

export async function createRun(
  project: string,
  service: string,
  body: CreateRunRequest,
): Promise<KusoRun> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/runs`,
    { method: "POST", body },
  );
}

// cancelRun stamps phase=cancelled and tears down the underlying
// Job. 204 on success; 400 if already terminal; 404 for unknown name.
export async function cancelRun(project: string, run: string): Promise<void> {
  await api(
    `/api/projects/${encodeURIComponent(project)}/runs/${encodeURIComponent(run)}/cancel`,
    { method: "POST" },
  );
}
