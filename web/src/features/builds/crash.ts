import { useQuery } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api-client";
import type { BuildFailureClass } from "@/features/services/api";

// Mirrors projects.CrashReport (server-go/internal/projects/crash.go): the
// env's latest container exit, the dying run's log tail and its
// classification.
export interface CrashReport {
  pod: string;
  container: string;
  restarts: number;
  reason?: string;
  exitCode: number;
  finishedAt?: string;
  log: string[];
  classification?: BuildFailureClass;
}

// getCrashReport: null when nothing has crashed, or when the server is
// too old to have the endpoint.
export async function getCrashReport(project: string, service: string, env: string): Promise<CrashReport | null> {
  try {
    const res = await api<{ crash: CrashReport | null }>(
      `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/crash?env=${encodeURIComponent(env)}`,
    );
    return res.crash ?? null;
  } catch (e) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 405)) return null;
    throw e;
  }
}

export function useCrashReport(project: string, service: string, env: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: ["crash-report", project, service, env ?? ""],
    queryFn: () => getCrashReport(project, service, env ?? ""),
    enabled: enabled && !!env,
    refetchInterval: 20_000,
    retry: 1,
  });
}

// exitMeaning names the common exit codes in plain words.
export function exitMeaning(code: number, reason?: string): string | undefined {
  if (reason === "OOMKilled" || code === 137) return "killed: out of memory or SIGKILL";
  if (code === 139) return "segmentation fault";
  if (code === 143) return "stopped by SIGTERM";
  if (code === 126) return "the start command isn't executable";
  if (code === 127) return "the start command wasn't found";
  if (code === 0) return "exited cleanly, but a service must keep running";
  return undefined;
}
