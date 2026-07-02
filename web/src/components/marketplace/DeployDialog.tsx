"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api-client";
import { useRenderApp, type MarketplaceApp, type RenderResult } from "@/features/marketplace";
import { applyConfig, type ConfigStepError } from "@/features/projects";

export function DeployDialog({ app, onClose }: { app: MarketplaceApp; onClose: () => void }) {
  const router = useRouter();
  const [project, setProject] = useState(app.name);
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [preview, setPreview] = useState<RenderResult | null>(null);
  const [deploying, setDeploying] = useState(false);
  const [applyErrors, setApplyErrors] = useState<ConfigStepError[] | null>(null);
  const render = useRenderApp(app.name);

  const missing = app.prompts.some((p) => p.required && !answers[p.key]);

  function invalidatePreview() {
    setPreview(null);
    setApplyErrors(null);
  }

  async function onPreview() {
    try {
      setPreview(await render.mutateAsync({ project, answers }));
      setApplyErrors(null);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function onDeploy() {
    if (!preview) return;
    setDeploying(true);
    try {
      try {
        // spec.Apply doesn't create the project; create it first.
        // 409 already-exists is fine; anything else re-throws.
        await api("/api/projects", { method: "POST", body: { name: project } });
      } catch (e) {
        if (!/409|exists/i.test((e as Error).message)) throw e;
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
      router.push(`/projects/${encodeURIComponent(project)}`);
    } catch (e) {
      toast.error((e as Error).message);
      setDeploying(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div className="w-full max-w-lg rounded-lg bg-[var(--surface)] p-5" onClick={(e) => e.stopPropagation()}>
        <h2 className="text-lg font-semibold">Deploy {app.title}</h2>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">{app.description}</p>

        <label className="mt-4 block text-sm">Project</label>
        <Input
          value={project}
          onChange={(e) => {
            setProject(e.target.value);
            invalidatePreview();
          }}
        />

        {app.prompts.map((p) => (
          <div key={p.key} className="mt-3">
            <label className="block text-sm">
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
          <div className="mt-4 rounded border border-[var(--border)] p-3 text-sm">
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
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          {!preview ? (
            <Button onClick={onPreview} disabled={missing || render.isPending}>
              {render.isPending ? "Rendering…" : "Preview"}
            </Button>
          ) : (
            <Button onClick={onDeploy} disabled={deploying}>
              {deploying ? "Deploying…" : "Deploy"}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
