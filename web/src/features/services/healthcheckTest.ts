import { api, ApiError } from "@/lib/api-client";

export interface HealthcheckTestRequest {
  path: string;
  port?: number;
  env?: string;
}

// One GET the server made against a pod. `error` (with no status) means
// the pod never answered; a 3xx carries `location` and is never followed.
export interface HealthcheckTestResult {
  status?: number;
  statusText?: string;
  latencyMs: number;
  bodySnippet?: string;
  location?: string;
  pod: string;
  url: string;
  error?: string;
}

export async function testHealthcheck(
  project: string,
  service: string,
  body: HealthcheckTestRequest,
): Promise<HealthcheckTestResult> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/healthcheck/test`,
    { method: "POST", body },
  );
}

// Servers older than the endpoint answer with the router's fallback
// ("no API route POST …"); a 404 that names a missing environment is a
// real answer and is shown as-is.
export function isHealthcheckTestUnsupported(e: unknown): boolean {
  if (!(e instanceof ApiError)) return false;
  if (e.status === 405) return true;
  return e.status === 404 && (e.message.startsWith("no API route") || e.message.startsWith("404"));
}

// Same rule kubelet applies: 200–399 counts as healthy.
export function healthcheckTestPassed(r: HealthcheckTestResult): boolean {
  return !r.error && r.status !== undefined && r.status >= 200 && r.status < 400;
}
