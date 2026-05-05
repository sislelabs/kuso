"use client";

import { Network, Plus, X } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Section, Row, type SectionProps } from "./_primitives";

// NetworkingSection — port + custom domains. Domains are per-host
// rows with explicit delete buttons (vs. the older free-text
// textarea) so a user can't accidentally clear the entire list by
// blur-then-save while traefik is flaky. Each row also has its own
// remove control so the intent is unambiguous: typing in a row
// edits ONE host; clicking + adds a new one; clicking ✕ removes
// just that host.
export function NetworkingSection({ state, setState }: SectionProps) {
  const lines = state.domains.split("\n").map((s) => s.trim());
  const hosts = lines.filter((s) => s.length > 0);

  const setHosts = (next: string[]) => {
    setState((s) => ({ ...s, domains: next.join("\n") }));
  };
  const updateAt = (i: number, value: string) => {
    // We keep empty rows in the editor while the user is typing
    // (so the input doesn't unmount under their cursor), but the
    // saved value drops them — see ServiceSettingsPanel.onSave.
    const next = [...lines];
    next[i] = value;
    setState((s) => ({ ...s, domains: next.join("\n") }));
  };
  const removeAt = (i: number) => {
    const next = [...lines];
    next.splice(i, 1);
    setHosts(next);
  };
  const append = () => {
    setState((s) => ({
      ...s,
      domains: s.domains ? s.domains + "\n" : "",
    }));
  };

  // Render at least one row even when the list is empty so the user
  // has somewhere to type. Save logic strips empties.
  const rows = lines.length === 0 ? [""] : lines;

  return (
    <Section id="networking" title="Networking" icon={Network}>
      <Row
        label="visibility"
        hint={
          state.internal
            ? "internal — reachable only from sibling pods via cluster DNS"
            : "public — Ingress + auto-domain + Let's Encrypt cert · check to flip to internal-only"
        }
        control={
          // Shrink-wrap the checkbox + short label so they stay
          // glued together at the right edge of the row. The full
          // explanation lives in the row's hint slot, which already
          // wraps gracefully under the label column.
          //
          // Earlier version had `Internal-only (no public Ingress)`
          // as the inline label; on the narrow control column it
          // wrapped after "no public" and the checkbox drifted left.
          <label className="inline-flex shrink-0 cursor-pointer items-center gap-2 whitespace-nowrap text-[12px]">
            <input
              type="checkbox"
              checked={state.internal}
              onChange={(e) =>
                setState((s) => ({ ...s, internal: e.target.checked }))
              }
              className="h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
            />
            <span>Internal-only</span>
          </label>
        }
      />
      <Row
        label="port"
        hint="container port"
        control={
          <Input
            type="number"
            value={state.port}
            onChange={(e) => setState((s) => ({ ...s, port: e.target.value }))}
            min={1}
            max={65535}
            className="h-7 w-24 font-mono text-[12px]"
          />
        }
      />
      <Row
        label="domains"
        hint={`${hosts.length} custom · auto-TLS via Let's Encrypt`}
        control={
          <div className="flex w-full max-w-[420px] flex-col gap-1.5">
            {rows.map((host, i) => (
              <div key={i} className="flex items-center gap-1.5">
                <Input
                  value={host}
                  onChange={(e) => updateAt(i, e.target.value)}
                  spellCheck={false}
                  placeholder="api.example.com"
                  className="h-7 flex-1 font-mono text-[12px]"
                />
                <button
                  type="button"
                  onClick={() => removeAt(i)}
                  aria-label="Remove domain"
                  className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              </div>
            ))}
            <Button
              variant="outline"
              size="xs"
              type="button"
              onClick={append}
              className="self-start"
            >
              <Plus className="h-3 w-3" />
              Add domain
            </Button>
          </div>
        }
        last
      />
    </Section>
  );
}
