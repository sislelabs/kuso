"use client";

import { Activity } from "lucide-react";
import { Input } from "@/components/ui/input";
import { useProject } from "@/features/projects";
import { cn } from "@/lib/utils";
import { Section, Row, type SectionProps } from "./_primitives";

export function UptimeSection({
  project,
  state,
  setState,
}: SectionProps & { project: string }) {
  const proj = useProject(project);
  const projectOff = proj.data?.project?.spec.uptime?.disabled === true;

  return (
    <Section id="uptime" title="Uptime" icon={Activity}>
      {projectOff && (
        <p className="border-b border-[var(--border-subtle)] px-3 py-2.5 text-[12px] text-[var(--text-secondary)]">
          Uptime checks are off for this project.
        </p>
      )}
      <Row
        label="uptime checks"
        hint="Ping this service once a minute and notify when it stops answering."
        control={
          <button
            type="button"
            disabled={projectOff}
            onClick={() => setState((s) => ({ ...s, uptimeEnabled: !s.uptimeEnabled }))}
            aria-pressed={state.uptimeEnabled}
            aria-label="Toggle uptime checks"
            className={cn(
              "inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors disabled:cursor-not-allowed disabled:opacity-50",
              state.uptimeEnabled
                ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)]"
                : "border-[var(--border-subtle)] bg-[var(--bg-tertiary)]",
            )}
          >
            <span
              className={cn(
                "inline-block h-3.5 w-3.5 rounded-full bg-white shadow transition-transform",
                state.uptimeEnabled ? "translate-x-4" : "translate-x-0.5",
              )}
            />
          </button>
        }
      />
      <Row
        label="check path"
        hint="Path the uptime check requests. Leave empty to use the health check path, or /."
        control={
          <Input
            value={state.uptimePath}
            onChange={(e) => setState((s) => ({ ...s, uptimePath: e.target.value }))}
            disabled={projectOff || !state.uptimeEnabled}
            spellCheck={false}
            placeholder="/"
            aria-label="Uptime check path"
            className="h-7 w-full max-w-[260px] font-mono text-[12px]"
          />
        }
        last
      />
    </Section>
  );
}
