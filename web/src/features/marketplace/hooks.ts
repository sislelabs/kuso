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

// useRenderPreview renders the template for the "what gets created"
// summary. Render is a pure server-side function (writes nothing), so it
// is safe to run as a query whenever the form is valid; callers debounce
// the inputs.
export function useRenderPreview(
  app: string,
  project: string,
  answers: Record<string, string>,
  enabled: boolean,
) {
  return useQuery({
    queryKey: ["marketplace", "render", app, project, answers],
    queryFn: () => renderApp(app, project, answers),
    enabled,
    retry: false,
    staleTime: 60_000,
  });
}
