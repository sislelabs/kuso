import { api, ApiError } from "@/lib/api-client";
import { triggerBuild } from "./api";
import type { CreateServiceBody } from "./createServiceBody";

export interface CreateServiceDeps {
  post: (project: string, body: CreateServiceBody) => Promise<unknown>;
  trigger: (project: string, service: string, branch?: string) => Promise<unknown>;
}

const defaultDeps: CreateServiceDeps = {
  post: (project, body) => api(`/api/projects/${encodeURIComponent(project)}/services`, { method: "POST", body }),
  trigger: (project, service, branch) => triggerBuild(project, service, branch ? { branch } : {}),
};

export interface CreateServiceOutcome {
  // "started": a first build is running (server- or client-triggered).
  // "failed": it should have started but didn't (buildError says why).
  // "none": nothing to build (image or worker runtime).
  // "skipped": the caller asked to create without deploying.
  build: "started" | "failed" | "none" | "skipped";
  buildError?: string;
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

// createServiceWithEnv creates a service with its variables and addon
// subscription in the one POST, then makes sure the first build runs.
// The production env is born with all of it, so the first deploy rolls
// once (PUTting them after the create rolled it a second time). The
// server stores envValues like the per-key auto write: plain values in
// the managed <service>-secrets Secret, never on the service CR.
//
// Current servers start the first build themselves and say so in
// `firstBuild`; for older ones (no field) the client triggers it. With
// deploy: false the server is asked to skip it (skipFirstBuild) and the
// client never starts one; a server too old to know the flag builds
// anyway and the outcome says "started".
export async function createServiceWithEnv(
  project: string,
  body: CreateServiceBody,
  opts: {
    envVars?: { name: string; value: string }[];
    // Addons whose connection variables the service mounts (short names).
    // Undefined = every project addon.
    subscribedAddons?: string[];
    // Runs after the service exists, before the client build fallback
    // (e.g. adding suggested addons).
    beforeBuild?: () => Promise<void>;
    // false = create only; the first deploy is left to the user.
    deploy?: boolean;
  } = {},
  deps: CreateServiceDeps = defaultDeps,
): Promise<CreateServiceOutcome> {
  const deploy = opts.deploy ?? true;
  const created = await deps.post(project, {
    ...body,
    ...(opts.envVars?.length ? { envValues: opts.envVars } : {}),
    ...(opts.subscribedAddons ? { subscribedAddons: opts.subscribedAddons } : {}),
    ...(deploy ? {} : { skipFirstBuild: true }),
  });
  const service = body.name;
  if (opts.beforeBuild) await opts.beforeBuild();

  const buildable = body.runtime !== "image" && body.runtime !== "worker" && !!body.repo?.url;
  if (!buildable) return { build: "none" };

  const fb = firstBuildOf(created);
  if (fb?.triggered) return { build: "started" };
  if (!deploy || fb?.skipped) return { build: "skipped" };
  if (fb?.error) return { build: "failed", buildError: fb.error };
  try {
    await deps.trigger(project, service, body.repo?.defaultBranch);
    return { build: "started" };
  } catch (e) {
    return { build: "failed", buildError: errMessage(e) };
  }
}
