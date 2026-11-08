import type { MarketplaceApp, MarketplacePrompt } from "./api";

const HOST_RE = /^(?=.{1,253}$)([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\.)+[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;

// promptError validates one answer the way the user will hit it at apply
// time. Returns null when it's acceptable.
export function promptError(p: MarketplacePrompt, value: string): string | null {
  const v = value.trim();
  if (!v) return p.required ? `${p.title} is required.` : null;
  if (p.kind === "domain") {
    if (/^[a-z]+:\/\//i.test(v)) return "Enter just the hostname, without http:// or https://.";
    if (/[/:]/.test(v)) return "Enter just the hostname, without a path or port.";
    if (!HOST_RE.test(v.toLowerCase())) return "Enter a valid hostname, e.g. status.example.com.";
  }
  return null;
}

// Prompts that need an answer from the user up-front: required ones with
// no default. Everything else (optional, or pre-filled) is "Advanced".
export function splitPrompts(app: MarketplaceApp): { required: MarketplacePrompt[]; advanced: MarketplacePrompt[] } {
  const required: MarketplacePrompt[] = [];
  const advanced: MarketplacePrompt[] = [];
  for (const p of app.prompts) {
    if (p.required && !p.default) required.push(p);
    else advanced.push(p);
  }
  return { required, advanced };
}

// Only answers with a value are sent; the server fills declared defaults
// and rejects undeclared keys.
export function answersPayload(
  app: MarketplaceApp,
  value: (p: MarketplacePrompt) => string,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const p of app.prompts) {
    const v = value(p).trim();
    if (v) out[p.key] = p.kind === "domain" ? v.toLowerCase() : v;
  }
  return out;
}

// landingPath opens the first created service on its Deployments tab so
// the first rollout is visible; addon-only apps open the addon instead.
export function landingPath(
  project: string,
  plan: { servicesToCreate: string[]; servicesToUpdate: string[]; addonsToCreate: string[]; addonsToUpdate: string[] },
): string {
  const base = `/projects/${encodeURIComponent(project)}`;
  const svc = plan.servicesToCreate[0] ?? plan.servicesToUpdate[0];
  if (svc) return `${base}?service=${encodeURIComponent(svc)}&tab=deployments`;
  const addon = plan.addonsToCreate[0] ?? plan.addonsToUpdate[0];
  if (addon) return `${base}?addon=${encodeURIComponent(addon)}`;
  return base;
}

// uniqueProjectName picks `base`, or base-2, base-3… when taken.
export function uniqueProjectName(base: string, taken: ReadonlySet<string>): string {
  if (!taken.has(base)) return base;
  for (let i = 2; i < 100; i++) {
    const candidate = `${base.slice(0, 37)}-${i}`;
    if (!taken.has(candidate)) return candidate;
  }
  return base;
}
