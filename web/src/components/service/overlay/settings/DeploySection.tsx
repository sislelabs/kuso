"use client";

import { Cloud } from "lucide-react";
import { useProject } from "@/features/projects";
import { Input } from "@/components/ui/input";
import { Section, Field, SwitchField, Callout, Code, monoInputClass, type SectionProps } from "./_primitives";

export function DeploySection({
  project,
  state,
  setState,
}: SectionProps & { project: string }) {
  // The project's previews config, so "will my PR get its own URL?" is
  // answered here instead of in project settings.
  const proj = useProject(project);
  type ProjSpec = { previews?: { enabled?: boolean; ttlDays?: number } };
  const spec = (proj.data as { project?: { spec?: ProjSpec } } | undefined)?.project?.spec;
  const previewsOn = !!spec?.previews?.enabled;
  const ttlDays = spec?.previews?.ttlDays ?? 7;
  const branch = state.repoBranch.trim() || "main";

  return (
    <Section
      id="deploy"
      title="Deploy"
      icon={Cloud}
      description={
        <>
          Successful builds of <Code>{branch}</Code> ship to production.
        </>
      }
      footerNote="A new start command rolls the pods in every environment."
    >
      <Field
        label="Start command"
        hint="Overrides the image's CMD. Split on spaces. Blank uses the image default."
        htmlFor="deploy-start-command"
      >
        <Input
          id="deploy-start-command"
          value={state.startCommand}
          onChange={(e) => setState((s) => ({ ...s, startCommand: e.target.value }))}
          placeholder="Image default"
          className={monoInputClass}
          spellCheck={false}
        />
      </Field>
      <SwitchField
        label="Wait for CI"
        hint="Hold builds from pushes and PR previews until the commit's GitHub checks pass. A failing check cancels the build."
        on={state.waitForCI}
        onChange={(v) => setState((s) => ({ ...s, waitForCI: v }))}
      />
      {previewsOn ? (
        <>
          <SwitchField
            label="Skip PR previews for this service"
            hint={`PR previews are on for this project: each PR gets its own environment, deleted when the PR closes or after ${ttlDays} idle days.`}
            on={state.previewsDisabled}
            onChange={(v) => setState((s) => ({ ...s, previewsDisabled: v }))}
          />
        </>
      ) : (
        <Callout>
          PR previews are off for this project.{" "}
          <a
            href={`/projects/${encodeURIComponent(project)}/settings`}
            className="text-[var(--text-primary)] underline underline-offset-2 hover:text-[var(--accent)]"
          >
            Turn them on in project settings
          </a>{" "}
          to give each pull request its own URL.
        </Callout>
      )}
    </Section>
  );
}
