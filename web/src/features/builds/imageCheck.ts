import { useQuery } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api-client";

// Mirrors imagecheck.Result (server-go/internal/imagecheck).
export interface ImageCheck {
  status: "found" | "not_found" | "unknown";
  detail?: string;
}

// checkImage asks the server whether repository:tag exists in its
// registry. Older servers (404) and refusals read as unknown: the check
// only ever adds a warning, it never blocks a deploy.
export async function checkImage(project: string, service: string, repository: string, tag: string): Promise<ImageCheck> {
  const q = new URLSearchParams({ repository, tag });
  try {
    return await api<ImageCheck>(
      `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/image-check?${q.toString()}`,
    );
  } catch (e) {
    if (e instanceof ApiError && [400, 403, 404, 405].includes(e.status)) return { status: "unknown" };
    throw e;
  }
}

export function useImageCheck(project: string, service: string, repository: string, tag: string, enabled: boolean) {
  return useQuery({
    queryKey: ["image-check", project, service, repository, tag],
    queryFn: () => checkImage(project, service, repository, tag),
    enabled: enabled && !!repository && !!tag,
    staleTime: 60_000,
    retry: false,
  });
}
