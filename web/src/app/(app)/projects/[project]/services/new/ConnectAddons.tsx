"use client";

import { addonLabel } from "@/components/addon/AddonIcon";
import type { KusoAddon } from "@/types/projects";

// Kinds an app talks to as its database: connected by default. Caches,
// queues and storage are opt-in, so a frontend doesn't get REDIS_URL or
// S3 credentials it never reads.
const DATABASE_KINDS = new Set(["postgres", "mysql", "mariadb", "mongodb", "clickhouse"]);

export interface ExistingAddon {
  short: string;
  kind: string;
}

export function existingAddons(addons: readonly KusoAddon[] | undefined, project: string): ExistingAddon[] {
  const prefix = `${project}-`;
  return (addons ?? [])
    .map((a) => ({
      short: a.metadata.name.startsWith(prefix) ? a.metadata.name.slice(prefix.length) : a.metadata.name,
      kind: a.spec.kind ?? "",
    }))
    .sort((a, b) => a.short.localeCompare(b.short));
}

export function connectedByDefault(kind: string): boolean {
  return DATABASE_KINDS.has(kind);
}

// subscriptionFor turns the checkboxes into the service's addon list.
// undefined = leave the server default (every addon, including ones
// added later), which is what "everything checked" means.
export function subscriptionFor(
  existing: readonly ExistingAddon[],
  connected: (a: ExistingAddon) => boolean,
  created: readonly string[],
): string[] | undefined {
  const on = existing.filter(connected).map((a) => a.short);
  if (on.length === existing.length) return undefined;
  return [...on, ...created];
}

// ConnectAddons asks, per existing project addon, whether the new service
// gets its connection variables (DATABASE_URL, REDIS_URL, …).
export function ConnectAddons({
  addons,
  isConnected,
  onToggle,
}: {
  addons: readonly ExistingAddon[];
  isConnected: (a: ExistingAddon) => boolean;
  onToggle: (short: string, on: boolean) => void;
}) {
  if (addons.length === 0) return null;
  return (
    <fieldset className="space-y-1.5">
      <legend className="text-xs font-medium text-[var(--text-primary)]">Connect to addons</legend>
      <p className="text-xs text-[var(--text-secondary)]">
        A connected addon gives the service its connection variables. Databases are on by default; change it later in
        Variables.
      </p>
      <ul className="flex flex-wrap gap-x-4 gap-y-1.5 pt-0.5">
        {addons.map((a) => {
          const id = `connect-addon-${a.short}`;
          return (
            <li key={a.short} className="flex items-center gap-2">
              <input
                id={id}
                type="checkbox"
                checked={isConnected(a)}
                onChange={(e) => onToggle(a.short, e.target.checked)}
                className="h-4 w-4 accent-[var(--accent)]"
              />
              <label htmlFor={id} className="text-[13px] text-[var(--text-primary)]">
                <span className="font-mono">{a.short}</span>
                {a.kind && <span className="text-[var(--text-secondary)]"> · {addonLabel(a.kind)}</span>}
              </label>
            </li>
          );
        })}
      </ul>
    </fieldset>
  );
}
