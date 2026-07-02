import { useMutation, useQuery } from "@tanstack/react-query";
import { listMarketplace, renderApp } from "./api";

export function useMarketplace() {
  return useQuery({ queryKey: ["marketplace"], queryFn: listMarketplace });
}

export function useRenderApp(app: string) {
  return useMutation({
    mutationFn: (vars: { project: string; answers: Record<string, string> }) =>
      renderApp(app, vars.project, vars.answers),
  });
}
