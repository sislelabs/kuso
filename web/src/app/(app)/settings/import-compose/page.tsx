"use client";

import { useState, useRef } from "react";
import { useMutation } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { env } from "@/lib/env";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { FileUp, CheckCircle2, AlertTriangle, Database, Package, Ban } from "lucide-react";
import { useCan, Perms } from "@/features/auth";
import { toast } from "sonner";

// docker-compose import wizard — convert-preview, then apply.
//
// Step 1: paste or upload a docker-compose.yml + name the target kuso
// project. POST /api/import/compose runs the converter server-side and
// returns the generated kuso.yaml plus a per-decision report. Nothing
// is written.
//
// Step 2: review the report (datastores → addons, image/build → service,
// flagged gaps), then Apply — which feeds the generated kuso.yaml back
// through the existing POST /api/projects/{p}/apply config-as-code path.
// The converter is the same code the `kuso import compose` CLI uses, so
// CLI and UI can never disagree.

type NoteAction = "service" | "addon" | "flag" | "skip";

interface Note {
  action: NoteAction;
  service: string;
  detail: string;
}

interface ComposeResponse {
  project: string;
  yaml: string;
  notes: Note[];
  flagged: boolean;
}

const ACTION_META: Record<NoteAction, { label: string; cls: string; icon: React.ReactNode }> = {
  service: { label: "service", cls: "text-emerald-300", icon: <CheckCircle2 className="h-3 w-3" /> },
  addon: { label: "addon", cls: "text-sky-300", icon: <Database className="h-3 w-3" /> },
  flag: { label: "flag", cls: "text-amber-300", icon: <AlertTriangle className="h-3 w-3" /> },
  skip: { label: "skip", cls: "text-[var(--text-tertiary)]", icon: <Ban className="h-3 w-3" /> },
};

export default function ImportComposePage() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const [project, setProject] = useState("");
  const [composeText, setComposeText] = useState("");
  const [applied, setApplied] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const preview = useMutation<ComposeResponse, Error>({
    mutationFn: () =>
      api<ComposeResponse>("/api/import/compose", {
        method: "POST",
        body: { project, compose: composeText },
      }),
    onSuccess: () => setApplied(false),
    onError: (err) => toast.error(err.message),
  });

  // Apply feeds the generated YAML to the config-as-code endpoint. It
  // can't go through api() (which JSON-wraps every body) — /apply reads
  // a raw kuso.yaml body — so this is a direct fetch.
  const apply = useMutation<void, Error, string>({
    mutationFn: async (yaml: string) => {
      const proj = preview.data?.project ?? project;
      // spec.Apply creates services/addons/crons but not the project
      // itself — ensure it exists first. 409 (already exists) is fine.
      await api("/api/projects", { method: "POST", body: { name: proj } }).catch(
        (e) => {
          const status = (e as { status?: number })?.status;
          if (status !== 409) throw e;
        },
      );
      const res = await fetch(
        `${env.apiBase}/api/projects/${encodeURIComponent(proj)}/apply`,
        {
          method: "POST",
          headers: { "Content-Type": "application/x-yaml" },
          body: yaml,
          credentials: "include",
        },
      );
      if (!res.ok) {
        throw new Error(`${res.status}: ${await res.text()}`);
      }
    },
    onSuccess: () => {
      setApplied(true);
      toast.success("Resources created — check the project dashboard");
    },
    onError: (err) => toast.error(err.message),
  });

  function onFile(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    file.text().then((t) => {
      setComposeText(t);
      // Default the project name from the file's directory-ish name.
      if (!project) {
        const base = file.name.replace(/\.(ya?ml)$/i, "");
        if (base && base !== "docker-compose" && base !== "compose") setProject(base);
      }
    });
  }

  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-2xl p-6 lg:p-8">
        <p className="text-sm text-[var(--text-secondary)]">
          The docker-compose import is admin-only. Ask a team admin to run it for you.
        </p>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-4xl p-6 lg:p-8">
      <header className="mb-6 flex items-start gap-3">
        <FileUp className="mt-1 h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">Import docker-compose</h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Convert a <code className="font-mono text-[12px]">docker-compose.yml</code> into a kuso
            project. Datastores (postgres, redis, …) become managed addons; app
            services become build or image services. Preview is read-only — nothing
            is created until you apply.
          </p>
        </div>
      </header>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          preview.mutate();
        }}
        className="space-y-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4"
      >
        <label className="block">
          <span className="block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            Project name
          </span>
          <Input
            value={project}
            onChange={(e) => setProject(e.target.value)}
            placeholder="my-app"
            className="mt-1 h-8 font-mono text-[13px]"
            required
          />
        </label>
        <label className="block">
          <span className="flex items-center justify-between font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            <span>docker-compose.yml</span>
            <button
              type="button"
              onClick={() => fileRef.current?.click()}
              className="text-[var(--text-secondary)] underline-offset-2 hover:underline"
            >
              upload file
            </button>
          </span>
          <input ref={fileRef} type="file" accept=".yml,.yaml" className="hidden" onChange={onFile} />
          <textarea
            value={composeText}
            onChange={(e) => setComposeText(e.target.value)}
            placeholder="services:&#10;  web:&#10;    build: .&#10;    ports: [&quot;80:3000&quot;]&#10;  db:&#10;    image: postgres:16"
            className="mt-1 h-48 w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-[12px] outline-none focus:border-[var(--border-strong)]"
            required
          />
        </label>
        <div className="flex justify-end">
          <Button type="submit" size="sm" disabled={preview.isPending}>
            {preview.isPending ? "Converting…" : "Preview import"}
          </Button>
        </div>
        {preview.isError && (
          <p className="rounded-md border border-red-500/40 bg-red-500/5 p-2 text-[12px] text-red-300">
            {preview.error.message}
          </p>
        )}
      </form>

      {preview.data && (
        <ResultView
          data={preview.data}
          applied={applied}
          applyPending={apply.isPending}
          onApply={() => apply.mutate(preview.data!.yaml)}
        />
      )}
    </div>
  );
}

function ResultView({
  data,
  applied,
  applyPending,
  onApply,
}: {
  data: ComposeResponse;
  applied: boolean;
  applyPending: boolean;
  onApply: () => void;
}) {
  const serviceCount = data.notes.filter((n) => n.action === "service" && n.detail.includes("→ runtime")).length;
  const addonCount = data.notes.filter((n) => n.action === "addon" && n.detail.includes("→ addon")).length;
  const grouped = groupByService(data.notes);

  return (
    <section className="mt-6 space-y-4">
      <header className="flex flex-wrap items-center gap-3 text-[12px]">
        <StatChip icon={<Package className="h-3 w-3" />} label="services" value={serviceCount} />
        <StatChip icon={<Database className="h-3 w-3" />} label="addons" value={addonCount} />
        <span className="ml-auto font-mono text-[10px] text-[var(--text-tertiary)]">
          project <span className="text-[var(--text-secondary)]">{data.project}</span>
        </span>
      </header>

      {data.flagged && (
        <p className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/5 p-2 text-[12px] text-amber-200">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            Some services need attention before they&apos;ll deploy (⚠ rows below) —
            most often a build service that needs its <code className="font-mono">repo:</code> set.
            You can still apply now and fill those in afterward.
          </span>
        </p>
      )}

      <div className="space-y-3">
        {grouped.map(([svc, notes]) => (
          <div key={svc} className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3">
            <h3 className="mb-2 font-mono text-[11px] uppercase tracking-widest text-[var(--text-tertiary)]">
              {svc === "" ? "(file)" : svc}
            </h3>
            <ul className="space-y-1">
              {notes.map((n, i) => {
                const m = ACTION_META[n.action];
                return (
                  <li key={i} className="flex items-start gap-2 text-[12px]">
                    <span className={`mt-0.5 flex shrink-0 items-center gap-1 ${m.cls}`}>
                      {m.icon}
                      <span className="font-mono text-[10px]">{m.label}</span>
                    </span>
                    <span className="text-[var(--text-secondary)]">{n.detail}</span>
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </div>

      <details className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3">
        <summary className="cursor-pointer font-mono text-[11px] uppercase tracking-widest text-[var(--text-tertiary)]">
          generated kuso.yaml
        </summary>
        <pre className="mt-2 overflow-x-auto rounded-md bg-[var(--bg-primary)] p-3 font-mono text-[11px] text-[var(--text-secondary)]">
          {data.yaml}
        </pre>
      </details>

      <div className="flex items-center justify-end gap-3">
        {applied && (
          <span className="flex items-center gap-1 text-[12px] text-emerald-300">
            <CheckCircle2 className="h-3.5 w-3.5" /> applied
          </span>
        )}
        <Button size="sm" onClick={onApply} disabled={applyPending || applied}>
          {applyPending ? "Applying…" : applied ? "Applied" : "Apply to kuso"}
        </Button>
      </div>
    </section>
  );
}

function groupByService(notes: Note[]): [string, Note[]][] {
  const map = new Map<string, Note[]>();
  for (const n of notes) {
    const arr = map.get(n.service) ?? [];
    arr.push(n);
    map.set(n.service, arr);
  }
  return Array.from(map.entries()).sort(([a], [b]) => {
    if (a === "") return -1;
    if (b === "") return 1;
    return a.localeCompare(b);
  });
}

function StatChip({ icon, label, value }: { icon: React.ReactNode; label: string; value: number }) {
  return (
    <span className="flex items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1 font-mono text-[11px]">
      <span className="text-[var(--text-tertiary)]">{icon}</span>
      <span className="text-[var(--text-primary)]">{value}</span>
      <span className="text-[var(--text-tertiary)]">{label}</span>
    </span>
  );
}
