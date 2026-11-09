"use client";

import { Cloud } from "lucide-react";
import { toast } from "sonner";
import { useProject } from "@/features/projects";
import { useService } from "@/features/services";
import { useSetAutoDeployPaused } from "@/features/builds";
import { useCanOnProject, Perms } from "@/features/auth";
import { toastError } from "@/lib/errors";
import { Input } from "@/components/ui/input";
import { Section, Field, SwitchField, Callout, Code, monoInputClass, type SectionProps } from "./_primitives";

export function DeploySection({
  project,
  service,
  state,
  setState,
}: SectionProps & { project: string; service?: string }) {
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
      {service && <AutoDeploySwitch project={project} service={service} branch={branch} />}
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

// AutoDeploySwitch saves on toggle instead of joining the form draft: it
// is an operational pause, and the Deployments tab's "Resume" flips the
// same field.
function AutoDeploySwitch({ project, service, branch }: { project: string; service: string; branch: string }) {
  const svc = useService(project, service);
  const setPaused = useSetAutoDeployPaused(project, service);
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const spec = svc.data?.spec;
  if (!spec || spec.runtime === "image" || (spec.runtime === "worker" && spec.fromService)) return null;
  const paused = !!spec.autoDeployPaused;
  return (
    <SwitchField
      label="Auto-deploy on push"
      hint={
        paused ? (
          <>
            Paused: pushes to <Code>{branch}</Code> don&apos;t build. Manual deploys and PR previews still run. Saves
            immediately.
          </>
        ) : (
          <>
            Pushes to <Code>{branch}</Code> build and deploy this service. Saves immediately.
          </>
        )
      }
      on={!paused}
      stateLabel={paused ? "Paused" : "Enabled"}
      disabled={!canWrite || setPaused.isPending}
      onChange={(on) =>
        setPaused.mutate(!on, {
          onSuccess: () => toast.success(on ? "Auto-deploy resumed" : "Auto-deploy paused"),
          onError: (e) => toastError(e, on ? "Couldn't resume auto-deploy" : "Couldn't pause auto-deploy"),
        })
      }
    />
  );
}
