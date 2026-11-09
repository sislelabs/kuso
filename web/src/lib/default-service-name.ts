import { serviceSlugError } from "@/features/projects/names";
import { slugifyServiceName } from "@/features/services/slug";
import { defaultServiceHost } from "./default-host";

// defaultServiceName keeps a new project's first service named after the
// project (so it gets the project's bare host) unless that name doesn't
// fit the 53-char Helm release budget, in which case "web".
export function defaultServiceName(project: string, repoName: string): string {
  const candidate = project || slugifyServiceName(repoName);
  if (candidate && !serviceSlugError(project || "x", slugifyServiceName(candidate))) return candidate;
  return "web";
}

// firstServiceHost is the host the server will assign a new project's
// first service, or undefined when it can't be known before create: no
// project base domain and the instance domain hasn't loaded (or the
// server is too old to say).
export function firstServiceHost(
  project: string,
  repoName: string,
  baseDomain: string,
  instanceDomain: string | undefined,
): string | undefined {
  if (!project) return undefined;
  if (!baseDomain.trim() && !instanceDomain) return undefined;
  const service = slugifyServiceName(defaultServiceName(project, repoName));
  return defaultServiceHost(service, project, baseDomain, instanceDomain ?? "");
}
