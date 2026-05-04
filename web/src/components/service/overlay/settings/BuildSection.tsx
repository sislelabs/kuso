"use client";

import { Hammer } from "lucide-react";
import { cn } from "@/lib/utils";
import { Section, Row, RUNTIMES, type SectionProps } from "./_primitives";

export function BuildSection({ state, setState }: SectionProps) {
  return (
    <Section id="build" title="Build" icon={Hammer}>
      <Row
        label="strategy"
        hint="how kuso builds the image"
        control={
          <div className="inline-flex flex-wrap gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
            {RUNTIMES.map((r) => (
              <button
                key={r}
                type="button"
                onClick={() => setState((s) => ({ ...s, runtime: r }))}
                className={cn(
                  "rounded px-2 py-1 font-mono text-[11px] transition-colors",
                  state.runtime === r
                    ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                    : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
                )}
              >
                {r}
              </button>
            ))}
          </div>
        }
        last
      />
    </Section>
  );
}
