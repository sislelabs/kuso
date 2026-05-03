import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// serviceShortName strips the project prefix off a KusoService.metadata.name
// (which is the FQN, e.g. "myapp-web") to get the short name the API
// + UI URLs use ("web"). The /api/projects/:p/services/:s endpoints
// take the SHORT name, not the FQN — passing the FQN 404s.
export function serviceShortName(project: string, fqnOrShort: string): string {
  const prefix = project + "-";
  if (fqnOrShort.startsWith(prefix)) {
    return fqnOrShort.slice(prefix.length);
  }
  return fqnOrShort;
}
