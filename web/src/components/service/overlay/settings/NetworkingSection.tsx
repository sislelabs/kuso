"use client";

import { Network, Plus, X, Globe, Lock } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Section, Row, type SectionProps } from "./_primitives";

// NetworkingSection — the single source of truth for "where does
// this service answer HTTP". Three blocks, in order:
//
//   1. visibility   — public Ingress vs. internal-only ClusterIP
//   2. port         — container port; we auto-inject PORT into the
//                     pod env so apps that read $PORT (Express,
//                     Flask, Rails, FastAPI, Spring Boot) bind here
//                     without the user having to add the var.
//   3. auto-domain  — read-only line showing the project's
//                     baseDomain-derived hostname. Lives next to
//                     custom-domains so the user sees both at once
//                     and stops asking "where does my service live"
//                     in the support channel.
//   4. custom domains — per-host rows, explicit add/remove.
//
// Pre-v0.9.5 this section showed only port + custom-domains, leaving
// users to guess the auto-domain from the canvas tooltip. The
// 502-Bad-Gateway / "I changed it but nothing happened" support
// volume came from this gap; the auto-domain row closes it.
export function NetworkingSection({ state, setState, autoHost }: SectionProps) {
  const lines = state.domains.split("\n").map((s) => s.trim());
  const hosts = lines.filter((s) => s.length > 0);

  const setHosts = (next: string[]) => {
    setState((s) => ({ ...s, domains: next.join("\n") }));
  };
  const updateAt = (i: number, value: string) => {
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
        hint="container port · kuso auto-sets $PORT for you"
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
      {!state.internal && autoHost && (
        <Row
          label="auto-domain"
          hint="set by project baseDomain · change in Project Settings to rename for every service"
          control={
            <a
              href={`https://${autoHost}`}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex max-w-[420px] items-center gap-1.5 truncate rounded-md bg-[var(--bg-tertiary)] px-2 py-1 font-mono text-[12px] text-[var(--text-secondary)] hover:text-[var(--accent)]"
              title={`Open https://${autoHost}`}
            >
              <Lock className="h-3 w-3 shrink-0" />
              <span className="truncate">{autoHost}</span>
            </a>
          }
        />
      )}
      <Row
        label="custom domains"
        hint={
          hosts.length === 0
            ? "point a DNS A-record at the cluster IP, then add the host below · auto-TLS via Let's Encrypt"
            : `${hosts.length} bound · DNS must point at the cluster IP for cert minting`
        }
        control={
          <div className="flex w-full max-w-[420px] flex-col gap-1.5">
            {rows.map((host, i) => (
              // Key on host text so removing a non-last row doesn't
              // make React match siblings to the deleted row's
              // controlled-input DOM. Empty rows fall back to index.
              <div key={host ? `h:${host}` : `empty:${i}`} className="flex items-center gap-1.5">
                <Globe className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
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
