import { ApiError } from "@/lib/api-client";

// Mirrors reservedRouteNames in server-go/internal/projects/projects_ops.go.
// "summary" is reserved for the API (/api/projects/summary), not a route.
export const RESERVED_PROJECT_NAMES = new Set([
  "new",
  "projects",
  "services",
  "addons",
  "envs",
  "logs",
  "settings",
  "invite",
  "summary",
]);

// Mirrors validateProjectName (≤40 chars, RFC 1123 label) and the
// reserved "kuso-" prefix. Returns null when the server would accept it.
export function projectNameError(name: string): string | null {
  if (!name) return "Project name is required.";
  if (name.length > 40) return "Project names are at most 40 characters.";
  if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name)) {
    return "Lowercase letters, digits, and dashes only; must start and end with a letter or digit.";
  }
  if (/^[0-9]/.test(name)) return "Project names must start with a letter.";
  if (RESERVED_PROJECT_NAMES.has(name)) {
    return `"${name}" is reserved — it collides with an app route. Pick another name.`;
  }
  if (name.startsWith("kuso-")) return `Names starting with "kuso-" are reserved for kuso itself.`;
  return null;
}

// projectSlugFromRepo derives a project name the server will accept from
// a repo name: ≤40 chars, starts with a letter, no reserved name or
// "kuso-" prefix, and not one of `taken` (adds -2, -3, …). The onboarding
// wizard has no rename field, so an invalid slug used to strand the user.
export function projectSlugFromRepo(repo: string, taken: ReadonlySet<string> = new Set()): string {
  let base = repo
    .toLowerCase()
    .replace(/[^a-z0-9-]/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-+|-+$/g, "");
  base = base.replace(/^(kuso-)+/, "");
  if (!base) base = "app";
  if (/^[0-9]/.test(base)) base = `app-${base}`;
  if (RESERVED_PROJECT_NAMES.has(base) || base === "kuso") base = `${base}-app`;
  const fit = (s: string, max: number) => s.slice(0, max).replace(/-+$/, "");
  let slug = fit(base, 40);
  for (let n = 2; taken.has(slug); n++) {
    const suffix = `-${n}`;
    slug = fit(base, 40 - suffix.length) + suffix;
  }
  return slug;
}

// Helm caps release names at 53 chars and the production env CR is
// named "<project>-<service>-production", so the service slug's budget
// depends on the project name length.
const HELM_RELEASE_MAX = 53;

export function serviceSlugError(project: string, slug: string): string | null {
  const envName = `${project}-${slug}-production`;
  if (envName.length > HELM_RELEASE_MAX) {
    const max = HELM_RELEASE_MAX - project.length - "--production".length;
    return max > 0
      ? `Name is too long for this project: the URL slug can be at most ${max} characters (it's ${slug.length}).`
      : "This project's name is too long to add services to.";
  }
  return null;
}

// The service display name regex from services_ops.go (displayNameRE).
export const SERVICE_DISPLAY_NAME_RE = /^[A-Za-z0-9 -]{1,60}$/;

// Turns a raw API error into text a user can act on. Server errors are
// wrapped domain sentinels ("projects: invalid: name must be …"), so the
// "<pkg>: <kind>: " prefix is noise; a bare "internal" carries nothing.
export function friendlyApiError(e: unknown, fallback: string): string {
  if (!(e instanceof Error)) return fallback;
  let msg = e.message.trim();
  msg = msg.replace(/^[a-z][a-z0-9-]*: (?:invalid|conflict|not found|forbidden): /i, "");
  if (!msg || /^internal( server error)?$/i.test(msg)) {
    const status = e instanceof ApiError ? ` (HTTP ${e.status})` : "";
    return `${fallback}: the server hit an internal error${status}. Check the kuso-server logs.`;
  }
  return msg.charAt(0).toUpperCase() + msg.slice(1);
}
