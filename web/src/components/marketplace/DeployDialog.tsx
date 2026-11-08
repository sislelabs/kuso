"use client";

import { useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, ChevronRight, Database, ExternalLink, Globe, KeyRound, Loader2, Server } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { api, ApiError } from "@/lib/api-client";
import { cn } from "@/lib/utils";
import { defaultServiceHost, useInstanceDomain } from "@/lib/default-host";
import {
  answersPayload,
  landingPath,
  renderApp,
  promptError,
  splitPrompts,
  uniqueProjectName,
  useRenderPreview,
  type MarketplaceApp,
  type MarketplacePrompt,
} from "@/features/marketplace";
import { applyConfig, useProjects, type ConfigStepError } from "@/features/projects";
import { projectNameError } from "@/features/projects/names";
import { useCan } from "@/features/auth";
import { AppIcon } from "./AppIcon";

type Target = "new" | "existing";

const NOTE_ICON: Record<string, React.ComponentType<{ className?: string }>> = {
  service: Server,
  addon: Database,
  secret: KeyRound,
  domain: Globe,
};

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

// A real instance hostname can seed a default domain; localhost / bare
// IP previews can't (the result wouldn't resolve).
function usableInstanceDomain(host: string): string {
  if (!host || !host.includes(".") || /^[\d.]+$/.test(host) || host.endsWith(".localhost")) return "";
  return host;
}

export function DeployDialog({
  app,
  defaultProject,
  onClose,
}: {
  app: MarketplaceApp;
  // Preselect this existing project as the deploy target.
  defaultProject?: string;
  onClose: () => void;
}) {
  const router = useRouter();
  const qc = useQueryClient();
  const projects = useProjects();
  const canCreateProject = useCan("projects:create");
  const instanceDomain = usableInstanceDomain(useInstanceDomain());
  const taken = useMemo(() => new Set((projects.data ?? []).map((p) => p.metadata.name)), [projects.data]);

  const [target, setTarget] = useState<Target>(defaultProject || !canCreateProject ? "existing" : "new");
  const [newName, setNewName] = useState<string | null>(null);
  const [existing, setExisting] = useState(defaultProject ?? "");
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [touched, setTouched] = useState<Record<string, boolean>>({});
  const [submitted, setSubmitted] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [deploying, setDeploying] = useState(false);
  const [applyErrors, setApplyErrors] = useState<ConfigStepError[] | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  // Resources an apply into an existing project would overwrite; the
  // second Deploy click is the explicit consent.
  const [overwrites, setOverwrites] = useState<string[] | null>(null);
  // Set once this dialog created the project, so a retry after a failed
  // apply doesn't trip over its own 409.
  const [createdProject, setCreatedProject] = useState<string | null>(null);

  // A ?project= that doesn't exist (or isn't visible) falls back to the
  // normal choice instead of a select pointing at nothing.
  useEffect(() => {
    if (!defaultProject || !projects.data || taken.has(defaultProject)) return;
    setExisting((cur) => (cur === defaultProject ? "" : cur));
  }, [defaultProject, projects.data, taken]);

  const suggestedName = uniqueProjectName(app.name, taken);
  const projectName = target === "new" ? (newName ?? suggestedName) : existing;
  const existingProject = projects.data?.find((p) => p.metadata.name === existing);
  const baseDomain = target === "existing" ? existingProject?.spec.baseDomain ?? "" : "";

  const { required, advanced } = splitPrompts(app);
  const firstDomainKey = app.prompts.find((p) => p.kind === "domain")?.key;

  const suggestion = (p: MarketplacePrompt): string => {
    if (p.key !== firstDomainKey || !projectName) return "";
    if (!instanceDomain && !baseDomain) return "";
    return defaultServiceHost(app.name, projectName, baseDomain, instanceDomain);
  };
  const valueOf = (p: MarketplacePrompt): string => answers[p.key] ?? (suggestion(p) || p.default || "");

  const projectError =
    target === "new"
      ? projectNameError(projectName) ?? (taken.has(projectName) ? `A project named "${projectName}" already exists. Pick another name or deploy into it.` : null)
      : existing
        ? null
        : "Choose a project.";
  const promptErrors = Object.fromEntries(app.prompts.map((p) => [p.key, promptError(p, valueOf(p))]));
  const valid = !projectError && Object.values(promptErrors).every((e) => e === null);
  const payload = answersPayload(app, valueOf);
  const showError = (key: string) => submitted || touched[key];

  // An Advanced field with an error opens the section so it's visible.
  const advancedHasError = advanced.some((p) => promptErrors[p.key] !== null);
  useEffect(() => {
    if (submitted && advancedHasError) setAdvancedOpen(true);
  }, [submitted, advancedHasError]);

  const debounced = useDebounced({ project: projectName, answers: payload, valid }, 400);
  const preview = useRenderPreview(app.name, debounced.project, debounced.answers, debounced.valid);

  const resetOutcome = () => {
    setApplyErrors(null);
    setFormError(null);
    setOverwrites(null);
  };

  async function onDeploy() {
    setSubmitted(true);
    if (!valid) return;
    const overwriteConfirmed = overwrites !== null;
    setDeploying(true);
    resetOutcome();
    const project = projectName;
    try {
      const rendered = await qc.fetchQuery({
        queryKey: ["marketplace", "render", app.name, project, payload],
        queryFn: () => renderApp(app.name, project, payload),
        staleTime: 60_000,
      });

      if (target === "new" && createdProject !== project) {
        try {
          await api("/api/projects", { method: "POST", body: { name: project } });
        } catch (e) {
          // Never merge into a project the user didn't pick.
          if (e instanceof ApiError && e.status === 409) {
            setFormError(`A project named "${project}" already exists. Pick another name, or choose it under Existing project.`);
            return;
          }
          throw e;
        }
        setCreatedProject(project);
      } else if (target === "existing" && !overwriteConfirmed) {
        const plan = await applyConfig(project, rendered.yaml, true);
        const clash = [
          ...plan.servicesToUpdate.map((s) => `service ${s}`),
          ...plan.addonsToUpdate.map((a) => `addon ${a}`),
          ...plan.cronsToUpdate.map((c) => `cron ${c}`),
        ];
        if (clash.length > 0) {
          setOverwrites(clash);
          return;
        }
      }

      const result = await applyConfig(project, rendered.yaml, false);
      if (result.errors && result.errors.length > 0) {
        setApplyErrors(result.errors);
        return;
      }
      toast.success(`${app.title} is deploying in ${project}`);
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["projects"], exact: true }),
        qc.invalidateQueries({ queryKey: ["projects", "summary"] }),
        qc.invalidateQueries({ queryKey: ["projects", project] }),
      ]);
      router.push(landingPath(project, result.plan));
    } catch (e) {
      setFormError(e instanceof Error && e.message ? e.message : "Deploy failed.");
    } finally {
      setDeploying(false);
    }
  }

  const dirty = newName !== null || existing !== (defaultProject ?? "") || Object.keys(answers).length > 0;

  const field = (p: MarketplacePrompt) => {
    const id = `mp-${p.key}`;
    const err = showError(p.key) ? promptErrors[p.key] : null;
    const sugg = suggestion(p);
    return (
      <div key={p.key}>
        <label htmlFor={id} className="block text-[13px] font-medium text-[var(--text-primary)]">
          {p.title}
          {!p.required && <span className="ml-1 font-normal text-[var(--text-tertiary)]">(optional)</span>}
        </label>
        <Input
          id={id}
          type={p.kind === "password" ? "password" : "text"}
          className={cn("mt-1.5", p.kind === "domain" && "font-mono")}
          placeholder={p.placeholder ? `e.g. ${p.placeholder}` : p.kind === "domain" ? "e.g. status.example.com" : undefined}
          value={valueOf(p)}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={err ? true : undefined}
          aria-describedby={`${id}-hint`}
          onChange={(e) => {
            setAnswers({ ...answers, [p.key]: e.target.value });
            resetOutcome();
          }}
          onBlur={() => setTouched({ ...touched, [p.key]: true })}
        />
        <p id={`${id}-hint`} className={cn("mt-1 text-xs", err ? "text-[var(--error)]" : "text-[var(--text-tertiary)]")}>
          {err ??
            (sugg && answers[p.key] === undefined
              ? `${p.help ? `${p.help} ` : ""}Pre-filled from your kuso domain; you can change it.`
              : p.help)}
        </p>
      </div>
    );
  };

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next && !deploying) onClose();
      }}
      disablePointerDismissal={dirty || deploying}
    >
      <DialogContent className="block max-h-[90vh] overflow-y-auto p-0 sm:max-w-lg" showCloseButton={!deploying}>
        <div className="flex items-start gap-3 border-b border-[var(--border-subtle)] p-5 pr-12">
          <AppIcon app={app.name} className="h-11 w-11" />
          <div className="min-w-0">
            <DialogTitle className="font-heading text-lg font-semibold tracking-tight text-[var(--text-primary)]">
              Deploy {app.title}
            </DialogTitle>
            <DialogDescription className="mt-0.5 text-[13px] text-[var(--text-secondary)]">{app.description}</DialogDescription>
            {(app.appVersion || app.website) && (
              <p className="mt-1.5 flex flex-wrap items-center gap-x-3 text-xs text-[var(--text-tertiary)]">
                {app.appVersion && <span className="font-mono">v{app.appVersion}</span>}
                {app.website && (
                  <a
                    href={app.website}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1 hover:text-[var(--text-primary)] hover:underline"
                  >
                    Website <ExternalLink className="h-3 w-3" aria-hidden />
                  </a>
                )}
              </p>
            )}
          </div>
        </div>

        <form
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            void onDeploy();
          }}
          className="space-y-5 p-5"
        >
          <fieldset>
            <legend className="text-[13px] font-medium text-[var(--text-primary)]">Project</legend>
            <div role="group" aria-label="Deploy target" className="mt-1.5 grid grid-cols-2 gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-0.5">
              {(["new", "existing"] as const).map((t) => (
                <button
                  key={t}
                  type="button"
                  aria-pressed={target === t}
                  disabled={t === "new" && !canCreateProject}
                  title={t === "new" && !canCreateProject ? "You don't have permission to create projects" : undefined}
                  onClick={() => {
                    setTarget(t);
                    resetOutcome();
                  }}
                  className={cn(
                    "h-8 rounded text-xs font-medium outline-none transition-colors focus-visible:ring-2 focus-visible:ring-[var(--ring)] disabled:cursor-not-allowed disabled:opacity-50",
                    target === t
                      ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)] shadow-[var(--shadow-sm)]"
                      : "text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
                  )}
                >
                  {t === "new" ? "New project" : "Existing project"}
                </button>
              ))}
            </div>
            {target === "new" ? (
              <Input
                aria-label="New project name"
                className="mt-2 font-mono"
                value={projectName}
                autoComplete="off"
                spellCheck={false}
                aria-invalid={projectError && (submitted || touched.project) ? true : undefined}
                aria-describedby="mp-project-hint"
                onChange={(e) => {
                  setNewName(e.target.value.toLowerCase());
                  resetOutcome();
                }}
                onBlur={() => setTouched({ ...touched, project: true })}
              />
            ) : (
              <Select
                aria-label="Existing project"
                value={existing}
                aria-invalid={projectError && submitted ? true : undefined}
                aria-describedby="mp-project-hint"
                onChange={(e) => {
                  setExisting(e.target.value);
                  resetOutcome();
                }}
                wrapperClassName="mt-2"
                className="font-mono"
                placeholder={projects.isPending ? "Loading projects…" : "Choose a project"}
              >
                {[...(projects.data ?? [])]
                  .sort((a, b) => a.metadata.name.localeCompare(b.metadata.name))
                  .map((p) => (
                    <option key={p.metadata.name} value={p.metadata.name}>
                      {p.metadata.name}
                    </option>
                  ))}
              </Select>
            )}
            <p
              id="mp-project-hint"
              className={cn(
                "mt-1 text-xs",
                projectError && (submitted || touched.project) ? "text-[var(--error)]" : "text-[var(--text-tertiary)]",
              )}
            >
              {projectError && (submitted || touched.project)
                ? projectError
                : target === "new"
                  ? "A new project just for this app. Lowercase letters, digits and dashes."
                  : "The app's services are added next to what's already there. Nothing is removed."}
            </p>
          </fieldset>

          {required.length > 0 && <div className="space-y-4">{required.map(field)}</div>}

          {advanced.length > 0 && (
            <div className="rounded-md border border-[var(--border-subtle)]">
              <button
                type="button"
                aria-expanded={advancedOpen}
                aria-controls="mp-advanced"
                onClick={() => setAdvancedOpen((o) => !o)}
                className="flex w-full items-center gap-1.5 rounded-md px-3 py-2.5 text-left text-[13px] font-medium text-[var(--text-primary)] outline-none hover:bg-[var(--bg-secondary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
              >
                <ChevronRight
                  aria-hidden
                  className={cn("h-4 w-4 text-[var(--text-tertiary)] motion-safe:transition-transform", advancedOpen && "rotate-90")}
                />
                Advanced
                <span className="font-normal text-[var(--text-tertiary)]">
                  {advanced.length} optional setting{advanced.length === 1 ? "" : "s"} with defaults
                </span>
              </button>
              {advancedOpen && (
                <div id="mp-advanced" className="space-y-4 border-t border-[var(--border-subtle)] p-3">
                  {advanced.map(field)}
                </div>
              )}
            </div>
          )}

          <div className="rounded-md bg-[var(--bg-secondary)] p-3" aria-live="polite">
            <p className="text-xs font-medium text-[var(--text-secondary)]">What gets created</p>
            {!valid ? (
              <p className="mt-1.5 text-xs text-[var(--text-tertiary)]">Fill in the fields above to see a summary.</p>
            ) : preview.isFetching && !preview.data ? (
              <p className="mt-1.5 inline-flex items-center gap-1.5 text-xs text-[var(--text-tertiary)]">
                <Loader2 className="h-3 w-3 motion-safe:animate-spin" aria-hidden /> Rendering…
              </p>
            ) : preview.isError ? (
              <p className="mt-1.5 text-xs text-[var(--error)]">
                {preview.error instanceof Error ? preview.error.message : "Couldn't render this app."}
              </p>
            ) : preview.data ? (
              <ul className="mt-1.5 space-y-1">
                {preview.data.notes.map((n, i) => {
                  const Icon = NOTE_ICON[n.kind] ?? Server;
                  return (
                    <li key={i} className="flex items-start gap-2 text-[13px] text-[var(--text-primary)]">
                      <Icon className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" aria-hidden />
                      <span className="min-w-0 break-words">{n.detail.charAt(0).toUpperCase() + n.detail.slice(1)}</span>
                    </li>
                  );
                })}
              </ul>
            ) : null}
          </div>

          {overwrites && (
            <div role="alert" className="flex gap-2 rounded-md border border-[var(--warning)]/40 bg-[var(--warning-subtle)] p-3 text-[13px]">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[var(--warning)]" aria-hidden />
              <div className="min-w-0">
                <p className="font-medium text-[var(--text-primary)]">This replaces resources already in {projectName}</p>
                <p className="mt-0.5 text-[var(--text-secondary)]">
                  {overwrites.join(", ")} will be updated to this app&apos;s settings. Deploy again to confirm, or use a
                  new project.
                </p>
              </div>
            </div>
          )}

          {formError && (
            <p role="alert" className="rounded-md border border-[var(--error)]/40 bg-[var(--error-subtle)] p-3 text-[13px] text-[var(--text-primary)]">
              {formError}
            </p>
          )}

          {applyErrors && applyErrors.length > 0 && (
            <div role="alert" className="rounded-md border border-[var(--error)]/40 bg-[var(--error-subtle)] p-3 text-[13px]">
              <p className="font-medium text-[var(--text-primary)]">
                {applyErrors.length} step{applyErrors.length === 1 ? "" : "s"} failed.
                {createdProject === projectName ? ` Project ${projectName} was created; deploying again reuses it.` : ""}
              </p>
              <ul className="mt-1 space-y-0.5">
                {applyErrors.map((e, i) => (
                  <li key={i} className="text-[var(--text-secondary)]">
                    <span className="font-mono text-[var(--text-tertiary)]">
                      {e.resource} {e.op}
                    </span>{" "}
                    {e.message}
                  </li>
                ))}
              </ul>
            </div>
          )}

          <div className="flex flex-col-reverse gap-2 border-t border-[var(--border-subtle)] pt-4 sm:flex-row sm:justify-end">
            <Button type="button" variant="ghost" onClick={onClose} disabled={deploying}>
              Cancel
            </Button>
            <Button type="submit" disabled={deploying}>
              {deploying ? (
                <>
                  <Loader2 className="h-4 w-4 motion-safe:animate-spin" aria-hidden /> Deploying…
                </>
              ) : overwrites ? (
                "Deploy anyway"
              ) : (
                `Deploy ${app.title}`
              )}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
