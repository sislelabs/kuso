"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { api, ApiError } from "@/lib/api-client";
import { useRenderApp, type MarketplaceApp, type RenderResult } from "@/features/marketplace";
import { applyConfig, type ConfigStepError } from "@/features/projects";

export function DeployDialog({ app, onClose }: { app: MarketplaceApp; onClose: () => void }) {
  const router = useRouter();
  const qc = useQueryClient();
  const [project, setProject] = useState(app.name);
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [preview, setPreview] = useState<RenderResult | null>(null);
  const [deploying, setDeploying] = useState(false);
  const [applyErrors, setApplyErrors] = useState<ConfigStepError[] | null>(null);
  // The target project already exists: deploying into it must be an
  // explicit choice, never a silent merge.
  const [projectExists, setProjectExists] = useState(false);
  const render = useRenderApp(app.name);

  const missing = app.prompts.some((p) => p.required && !answers[p.key]);

  function invalidatePreview() {
    setPreview(null);
    setApplyErrors(null);
    setProjectExists(false);
  }

  async function onPreview() {
    try {
      setPreview(await render.mutateAsync({ project, answers }));
      setApplyErrors(null);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function onDeploy(intoExisting: boolean) {
    if (!preview) return;
    setDeploying(true);
    try {
      try {
        // spec.Apply doesn't create the project; create it first.
        await api("/api/projects", { method: "POST", body: { name: project } });
      } catch (e) {
        if (!(e instanceof ApiError && e.status === 409)) throw e;
        if (!intoExisting) {
          setProjectExists(true);
          setDeploying(false);
          return;
        }
      }
      const result = await applyConfig(project, preview.yaml, false);
      if (result.errors && result.errors.length > 0) {
        setApplyErrors(result.errors);
        toast.error(
          `${result.errors.length} step(s) failed: ${result.errors
            .map((e) => `${e.resource} ${e.op}: ${e.message}`)
            .join("; ")}`,
        );
        setDeploying(false);
        return;
      }
      toast.success(`Deployed ${app.title}`);
      // The project list and this project's describe may be cached from
      // an earlier visit; without this the canvas lands empty.
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["projects"], exact: true }),
        qc.invalidateQueries({ queryKey: ["projects", "summary"] }),
        qc.invalidateQueries({ queryKey: ["projects", project] }),
      ]);
      router.push(`/projects/${encodeURIComponent(project)}`);
    } catch (e) {
      toast.error((e as Error).message);
      setDeploying(false);
    }
  }

  // Typed answers or a renamed project are worth protecting from a stray
  // backdrop click; Escape and the explicit Cancel still close.
  const dirty = project !== app.name || Object.keys(answers).length > 0;

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next && !deploying) onClose();
      }}
      disablePointerDismissal={dirty || deploying}
    >
      <DialogContent
        className="block max-h-[90vh] overflow-y-auto p-5 sm:max-w-lg"
        showCloseButton={!deploying}
      >
        <DialogTitle className="text-lg font-semibold text-[var(--text-primary)]">
          Deploy {app.title}
        </DialogTitle>
        <DialogDescription className="mt-1 text-sm text-[var(--text-secondary)]">
          {app.description}
        </DialogDescription>

        <label className="mt-4 block text-sm text-[var(--text-secondary)]">Project</label>
        <Input
          value={project}
          onChange={(e) => {
            setProject(e.target.value);
            invalidatePreview();
          }}
          aria-invalid={projectExists ? true : undefined}
        />
        {projectExists && (
          <div role="alert" className="mt-1 flex flex-wrap items-center gap-2 text-xs text-[var(--error)]">
            <span>Project exists. Rename it, or</span>
            <button
              type="button"
              onClick={() => onDeploy(true)}
              disabled={deploying}
              className="font-medium underline underline-offset-2 hover:text-[var(--text-primary)]"
            >
              deploy into existing project
            </button>
          </div>
        )}

        {app.prompts.map((p) => (
          <div key={p.key} className="mt-3">
            <label className="block text-sm text-[var(--text-secondary)]">
              {p.title}
              {p.required && <span className="text-amber-400"> *</span>}
            </label>
            <Input
              type={p.kind === "password" ? "password" : "text"}
              placeholder={p.placeholder}
              value={answers[p.key] ?? p.default ?? ""}
              onChange={(e) => {
                setAnswers({ ...answers, [p.key]: e.target.value });
                invalidatePreview();
              }}
            />
            {p.help && <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">{p.help}</p>}
          </div>
        ))}

        {preview && (
          <div className="mt-4 rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3 text-sm">
            <p className="mb-1 font-medium">This will create:</p>
            <ul className="space-y-0.5">
              {preview.notes.map((n, i) => (
                <li key={i} className="text-[var(--text-secondary)]">
                  <span className="text-[var(--text-tertiary)]">[{n.kind}]</span> {n.detail}
                </li>
              ))}
            </ul>
          </div>
        )}

        {applyErrors && applyErrors.length > 0 && (
          <div className="mt-4 rounded border border-red-500/40 bg-red-500/10 p-3 text-sm">
            <p className="mb-1 font-medium text-amber-400">
              {applyErrors.length} step(s) failed:
            </p>
            <ul className="space-y-0.5">
              {applyErrors.map((e, i) => (
                <li key={i} className="text-red-400">
                  <span className="text-[var(--text-tertiary)]">
                    [{e.resource} {e.op}]
                  </span>{" "}
                  {e.message}
                </li>
              ))}
            </ul>
          </div>
        )}

        <div className="mt-5 flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={deploying}>Cancel</Button>
          {!preview ? (
            <Button onClick={onPreview} disabled={missing || render.isPending}>
              {render.isPending ? "Rendering…" : "Preview"}
            </Button>
          ) : (
            <Button onClick={() => onDeploy(false)} disabled={deploying || projectExists}>
              {deploying ? "Deploying…" : "Deploy"}
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
