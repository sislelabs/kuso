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
            : "public — Ingress + auto-domain + Let's Encrypt cert"
        }
        control={
          <button
            type="button"
            role="switch"
            aria-checked={state.internal}
            aria-label="Internal-only"
            onClick={() =>
              setState((s) => ({ ...s, internal: !s.internal }))
            }
            className="group inline-flex shrink-0 cursor-pointer items-center gap-2 whitespace-nowrap text-[12px] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]/40 rounded-md px-1 py-0.5"
          >
            <span
              className={`relative h-5 w-9 rounded-full transition-colors ${
                state.internal
                  ? "bg-[var(--accent)]"
                  : "bg-[var(--bg-tertiary)] border border-[var(--border)]"
              }`}
            >
              <span
                className={`absolute top-0.5 h-4 w-4 rounded-full bg-white shadow-sm transition-transform ${
                  state.internal ? "translate-x-4" : "translate-x-0.5"
                }`}
              />
            </span>
            <span
              className={
                state.internal
                  ? "text-[var(--text-primary)]"
                  : "text-[var(--text-secondary)] group-hover:text-[var(--text-primary)]"
              }
            >
              Internal-only
            </span>
          </button>
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
            : `${hosts.length} bound · DNS must point at cluster IP · if your app redirects to the auto-domain, set NEXTAUTH_URL / AUTH_URL / APP_URL / etc. to the custom host`
        }
        control={
          <div className="flex w-full max-w-[420px] flex-col gap-1.5">
            {rows.map((host, i) => (
              // Key on row index. We previously keyed on the typed
              // host value, which made React unmount + remount the
              // <input> on every keystroke (because `host` was the
              // value-being-typed) — net effect was the input lost
              // focus after one character. Index-keyed is the
              // correct shape for a list-of-strings editor: removal
              // shifts later rows up by one, which is exactly the
              // semantic the user expects ("delete this row, the
              // ones below take its place").
              <div key={`row-${i}`} className="flex items-center gap-1.5">
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
