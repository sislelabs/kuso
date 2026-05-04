"use client";

import { Cloud } from "lucide-react";
import { useProject } from "@/features/projects";
import { cn } from "@/lib/utils";
import { Section, Row, type SectionProps } from "./_primitives";

export function DeploySection({
  project,
  state,
  setState,
}: SectionProps & { project: string }) {
  // Read the project's previews config so the user sees, in this
  // service's settings, whether PR previews are on for the whole
  // project + how long they live. Saves them digging through the
  // project settings tab to answer "will my PR get its own URL?"
  const proj = useProject(project);
  type ProjSpec = { previews?: { enabled?: boolean; ttlDays?: number } };
  const spec = (proj.data as { project?: { spec?: ProjSpec } } | undefined)?.project?.spec;
  const previewsOn = !!spec?.previews?.enabled;
  const ttlDays = spec?.previews?.ttlDays ?? 7;

  return (
    <Section id="deploy" title="Deploy" icon={Cloud}>
      <div className="space-y-1 px-3 py-2.5 text-[12px] text-[var(--text-secondary)]">
        <p>
          Successful builds of <span className="font-mono">main</span> ship to{" "}
          <span className="font-mono">production</span>.
        </p>
        {previewsOn ? (
          <p>
            PR previews <span className="text-emerald-400">on</span> for this project —
            every PR gets a throwaway env at{" "}
            <span className="font-mono">
              &lt;service&gt;-pr-N.&lt;project-domain&gt;
            </span>
            , auto-deleted after the PR closes or {ttlDays} days idle. Previews boot
            with no env vars (set per-env if needed).
          </p>
        ) : (
          <p>
            PR previews <span className="text-[var(--text-tertiary)]">off</span> for
            this project. Enable in{" "}
            <a
              href={`/projects/${encodeURIComponent(project)}/settings`}
              className="text-[var(--accent)] hover:underline"
            >
              project settings
            </a>{" "}
            to give each PR its own URL.
          </p>
        )}
      </div>
      {previewsOn && (
        <Row
          label="exclude from previews"
          hint="skip PR previews for this service even when the project toggle is on"
          control={
            <button
              type="button"
              onClick={() =>
                setState((s) => ({ ...s, previewsDisabled: !s.previewsDisabled }))
              }
              aria-pressed={state.previewsDisabled}
              aria-label="Toggle preview opt-out"
              className={cn(
                "inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors",
                state.previewsDisabled
                  ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)]"
                  : "border-[var(--border-subtle)] bg-[var(--bg-tertiary)]",
              )}
            >
              <span
                className={cn(
                  "inline-block h-3.5 w-3.5 rounded-full bg-white shadow transition-transform",
                  state.previewsDisabled ? "translate-x-4" : "translate-x-0.5",
                )}
              />
            </button>
          }
          last
        />
      )}
    </Section>
  );
}
