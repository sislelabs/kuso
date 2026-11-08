"use client";

import { Activity } from "lucide-react";
import { Input } from "@/components/ui/input";
import { useProject } from "@/features/projects";
import { Section, Field, SwitchField, Callout, monoInputClass, type SectionProps } from "./_primitives";

export function UptimeSection({
  project,
  state,
  setState,
}: SectionProps & { project: string }) {
  const proj = useProject(project);
  const projectOff = proj.data?.project?.spec.uptime?.disabled === true;

  return (
    <Section
      id="uptime"
      title="Uptime"
      icon={Activity}
      status={projectOff ? "Off for project" : state.uptimeEnabled ? "Checked every minute" : "Not checked"}
      description="Ping this service once a minute and notify when it stops answering."
    >
      {projectOff && <Callout>Uptime checks are off for the whole project, so these settings have no effect.</Callout>}
      <SwitchField
        label="Uptime checks"
        on={state.uptimeEnabled}
        disabled={projectOff}
        onChange={(v) => setState((s) => ({ ...s, uptimeEnabled: v }))}
      />
      <Field label="Check path" hint="Blank uses the health check path, or /." htmlFor="uptime-path">
        <Input
          id="uptime-path"
          value={state.uptimePath}
          onChange={(e) => setState((s) => ({ ...s, uptimePath: e.target.value }))}
          disabled={projectOff || !state.uptimeEnabled}
          spellCheck={false}
          placeholder="/"
          className={monoInputClass}
        />
      </Field>
    </Section>
  );
}
