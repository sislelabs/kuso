import { useMutation, useQuery } from "@tanstack/react-query";
import { listMarketplace, renderApp } from "./api";

export function useMarketplace() {
  return useQuery({ queryKey: ["marketplace"], queryFn: listMarketplace });
}

export function useRenderApp(app: string) {
  return useMutation({
    // DeployDialog mutateAsyncs this in a try/catch and toasts itself.
    meta: { skipGlobalErrorToast: true },
    mutationFn: (vars: { project: string; answers: Record<string, string> }) =>
      renderApp(app, vars.project, vars.answers),
  });
}
