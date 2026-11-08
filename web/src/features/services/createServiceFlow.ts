import { api, ApiError } from "@/lib/api-client";
import { setServiceEnvValue, triggerBuild } from "./api";
import type { CreateServiceBody } from "./createServiceBody";

export interface CreateServiceDeps {
  post: (project: string, body: CreateServiceBody) => Promise<unknown>;
  setEnv: (project: string, service: string, name: string, value: string) => Promise<unknown>;
  trigger: (project: string, service: string, branch?: string) => Promise<unknown>;
}

const defaultDeps: CreateServiceDeps = {
  post: (project, body) => api(`/api/projects/${encodeURIComponent(project)}/services`, { method: "POST", body }),
  setEnv: setServiceEnvValue,
  trigger: (project, service, branch) => triggerBuild(project, service, branch ? { branch } : {}),
};

export interface CreateServiceOutcome {
  // "started": a first build is running (server- or client-triggered).
  // "failed": it should have started but didn't (buildError says why).
  // "none": nothing to build (image or worker runtime).
  // "skipped": the caller asked to create without deploying.
  build: "started" | "failed" | "none" | "skipped";
  buildError?: string;
  // Variables that didn't save; the service exists without them.
  envFailed: { name: string; error: string }[];
}

function firstBuildOf(created: unknown): { triggered?: boolean; skipped?: boolean; error?: string } | undefined {
  if (!created || typeof created !== "object") return undefined;
  const fb = (created as Record<string, unknown>).firstBuild;
  if (!fb || typeof fb !== "object") return undefined;
  const r = fb as Record<string, unknown>;
  return {
    triggered: typeof r.triggered === "boolean" ? r.triggered : undefined,
    skipped: r.skipped === true,
    error: typeof r.error === "string" ? r.error : undefined,
  };
}

function errMessage(e: unknown): string {
  if (e instanceof ApiError || e instanceof Error) return e.message;
  return "unknown error";
}

// createServiceWithEnv creates a service, stores the pasted variables, and
// makes sure the first build runs. The create POST throws on failure (the
// service doesn't exist); everything after it is best-effort and reported
// in the outcome, because the service is already there.
//
// Variables go through the per-key auto-storage endpoint rather than the
// create body's envVars: the create body persists values as plaintext on
// the service CR, while auto storage keeps secrets in the managed
// <service>-secrets Secret. Runtime env only matters once the first build
// deploys, which is minutes after these few requests finish.
//
// Current servers start the first build themselves and say so in
// `firstBuild`; for older ones (no field) the client triggers it, after
// the variables are in place. With deploy: false the server is asked to
// skip it (skipFirstBuild) and the client never starts one; a server too
// old to know the flag builds anyway and the outcome says "started".
export async function createServiceWithEnv(
  project: string,
  body: CreateServiceBody,
  opts: {
    envVars?: { name: string; value: string }[];
    // Runs after the service and its variables exist, before the client
    // build fallback (e.g. adding suggested addons).
    beforeBuild?: () => Promise<void>;
    // false = create only; the first deploy is left to the user.
    deploy?: boolean;
  } = {},
  deps: CreateServiceDeps = defaultDeps,
): Promise<CreateServiceOutcome> {
  const deploy = opts.deploy ?? true;
  const created = await deps.post(project, deploy ? body : { ...body, skipFirstBuild: true });
  const service = body.name;

  const envFailed: CreateServiceOutcome["envFailed"] = [];
  // Sequential: every write read-modify-writes the same Secret.
  for (const v of opts.envVars ?? []) {
    try {
      await deps.setEnv(project, service, v.name, v.value);
    } catch (e) {
      envFailed.push({ name: v.name, error: errMessage(e) });
    }
  }
  if (opts.beforeBuild) await opts.beforeBuild();

  const buildable = body.runtime !== "image" && body.runtime !== "worker" && !!body.repo?.url;
  if (!buildable) return { build: "none", envFailed };

  const fb = firstBuildOf(created);
  if (fb?.triggered) return { build: "started", envFailed };
  if (!deploy || fb?.skipped) return { build: "skipped", envFailed };
  if (fb?.error) return { build: "failed", buildError: fb.error, envFailed };
  try {
    await deps.trigger(project, service, body.repo?.defaultBranch);
    return { build: "started", envFailed };
  } catch (e) {
    return { build: "failed", buildError: errMessage(e), envFailed };
  }
}
