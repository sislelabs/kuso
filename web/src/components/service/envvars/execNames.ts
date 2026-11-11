import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";

// Env var names that load or choose code (NODE_OPTIONS, LD_PRELOAD,
// NIXPACKS_START_CMD, …). The server owns the list
// (projects.IsExecEnvName); this only fetches it so the editor can say up
// front that changing one needs the project admin role.
export interface ExecEnvNames {
  names: string[];
  prefixes: string[];
}

export function isExecEnvName(list: ExecEnvNames | undefined, name: string): boolean {
  if (!list) return false;
  const n = name.trim().toUpperCase();
  if (!n) return false;
  return list.names.includes(n) || list.prefixes.some((p) => n.startsWith(p));
}

export const EXEC_ENV_HINT =
  "Only a project admin can set or change this: it chooses code that runs with the service's secrets.";

export function useExecEnvNames(enabled: boolean) {
  return useQuery({
    queryKey: ["config", "exec-env-names"],
    queryFn: () => api<ExecEnvNames>("/api/config/exec-env-names"),
    enabled,
    staleTime: Infinity,
  });
}
