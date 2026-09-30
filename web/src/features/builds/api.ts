import { api } from "@/lib/api-client";

function buildPath(project: string, service: string, build: string): string {
  return `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/builds/${encodeURIComponent(build)}`;
}

// rollbackToBuild re-points the env at this build's image. force skips
// the server's branch-mismatch guard (400 without it).
export async function rollbackToBuild(
  project: string,
  service: string,
  build: string,
  env: string,
  force = false,
): Promise<unknown> {
  const qs = env ? `?env=${encodeURIComponent(env)}` : "";
  return api(`${buildPath(project, service, build)}/rollback${qs}`, {
    method: "POST",
    body: force ? { force: true } : undefined,
  });
}

export interface RetryReleaseResult {
  job: string;
}

export async function retryRelease(
  project: string,
  service: string,
  build: string,
): Promise<RetryReleaseResult> {
  return api(`${buildPath(project, service, build)}/retry-release`, { method: "POST" });
}
