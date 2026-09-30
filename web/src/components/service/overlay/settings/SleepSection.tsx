"use client";

import { Moon } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Section, Row, type SectionProps } from "./_primitives";

// SleepSection edits scale-to-zero. sleep.enabled governs the production
// env only; every non-production env (named envs, env-group clones, PR
// previews) sleeps by default unless the service sets
// sleep.nonProduction=off, which is CLI-only for now.
export function SleepSection({ state, setState }: SectionProps) {
  const minZero = Number(state.scaleMin) === 0;
  const prodOn = minZero || state.sleepEnabled;
  const keptWarm = state.sleepExcludePaths.trim() !== "";
  const nonProdOn = state.sleepNonProduction !== "off";
  const hint = keptWarm
    ? "kept warm by keep-warm paths"
    : `after ${state.sleepAfter || "30"}m idle`;
  return (
    <Section id="sleep" title="Sleep" icon={Moon} hint={hint}>
      <Row
        label="production"
        hint={
          minZero
            ? "min replicas is 0, so production always sleeps when idle"
            : "scale to zero when idle; the next request wakes it (cold start of a few seconds)"
        }
        control={
          <button
            type="button"
            role="switch"
            aria-checked={prodOn}
            aria-label="Sleep production when idle"
            disabled={minZero}
            onClick={() => setState((s) => ({ ...s, sleepEnabled: !s.sleepEnabled }))}
            className="group inline-flex shrink-0 cursor-pointer items-center gap-2 whitespace-nowrap rounded-md px-1 py-0.5 text-[12px] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]/40 disabled:cursor-not-allowed disabled:opacity-60"
          >
            <span
              className={`relative inline-block h-5 w-10 rounded-full transition-colors ${
                prodOn
                  ? "bg-[var(--accent)]"
                  : "border border-[var(--border)] bg-[var(--bg-tertiary)]"
              }`}
            >
              <span
                className={`absolute left-0 top-0.5 h-4 w-4 rounded-full bg-white shadow-sm transition-transform ${
                  prodOn ? "translate-x-[22px]" : "translate-x-[2px]"
                }`}
              />
            </span>
            <span className="text-[var(--text-secondary)] group-hover:text-[var(--text-primary)]">
              {prodOn ? "Sleeps when idle" : "Always on"}
            </span>
          </button>
        }
      />
      <Row
        label="non-production"
        hint="named envs, env-group clones and PR previews sleep by default · opt out with kuso project service sleep … --non-production off"
        control={
          <span className="font-mono text-[12px] text-[var(--text-secondary)]">
            {nonProdOn ? "sleeps when idle (default)" : "always on"}
          </span>
        }
      />
      <Row
        label="idle window"
        hint="minutes without a request before an env sleeps (production and non-production)"
        control={
          <div className="inline-flex items-center gap-1.5">
            <Input
              type="number"
              value={state.sleepAfter}
              onChange={(e) => setState((s) => ({ ...s, sleepAfter: e.target.value }))}
              className="h-7 w-20 font-mono text-[12px]"
              min={1}
            />
            <span className="font-mono text-[11px] text-[var(--text-tertiary)]">min</span>
          </div>
        }
      />
      {/* Any request to a listed path keeps the WHOLE deployment warm,
          so a webhook/callback doesn't hit a cold start. */}
      <Row
        label="keep-warm paths"
        hint="one per line · while any are set, this service never sleeps"
        control={
          <textarea
            value={state.sleepExcludePaths}
            onChange={(e) => setState((s) => ({ ...s, sleepExcludePaths: e.target.value }))}
            placeholder={"/api/webhooks/stripe\n/api/callbacks/github"}
            rows={2}
            aria-label="Keep-warm paths"
            className="h-auto w-56 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1 font-mono text-[11px]"
          />
        }
        last
      />
    </Section>
  );
}
