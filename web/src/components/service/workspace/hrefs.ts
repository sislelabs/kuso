// Links to the full-page service and addon views. Service and addon
// names are short names ("web", "db"); env is the env-group short name
// and is left out for production so links stay canonical.

export function servicePageHref(
  project: string,
  service: string,
  opts: { env?: string; tab?: string; highlight?: string; kind?: string } = {},
): string {
  const q = new URLSearchParams();
  if (opts.env && opts.env !== "production") q.set("env", opts.env);
  if (opts.tab) q.set("tab", opts.tab);
  if (opts.kind) q.set("kind", opts.kind);
  if (opts.highlight) q.set("highlight", opts.highlight);
  const qs = q.toString();
  return `/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}${qs ? `?${qs}` : ""}`;
}

export function addonPageHref(project: string, addon: string, opts: { env?: string; tab?: string } = {}): string {
  const short = addon.startsWith(`${project}-`) ? addon.slice(project.length + 1) : addon;
  const q = new URLSearchParams();
  if (opts.env && opts.env !== "production") q.set("env", opts.env);
  if (opts.tab) q.set("tab", opts.tab);
  const qs = q.toString();
  return `/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(short)}${qs ? `?${qs}` : ""}`;
}

export function projectCanvasHref(project: string, env?: string): string {
  const base = `/projects/${encodeURIComponent(project)}`;
  return env && env !== "production" ? `${base}?env=${encodeURIComponent(env)}` : base;
}

// resourceFromPath reads /projects/<p>/services/<s> and
// /projects/<p>/addons/<a>. Positional on purpose: useRouteParams skips
// segments that look like static route names, which would lose a service
// called "logs" or "settings".
export function resourceFromPath(
  pathname: string | null,
): { project: string; kind: "service" | "addon"; name: string } | null {
  if (!pathname) return null;
  const seg = pathname.replace(/^\/+|\/+$/g, "").split("/");
  if (seg.length !== 4 || seg[0] !== "projects") return null;
  if (seg[2] !== "services" && seg[2] !== "addons") return null;
  const project = safeDecode(seg[1]);
  const name = safeDecode(seg[3]);
  if (!project || !name || project === "_" || name === "_") return null;
  return { project, kind: seg[2] === "services" ? "service" : "addon", name };
}

function safeDecode(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}
