"use client";

import { useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Archive } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import {
  importProjectArchive,
  projectsQueryKey,
  projectsSummaryQueryKey,
  type ImportPolicy,
} from "@/features/projects";
import { friendlyApiError } from "@/features/projects/names";
import { cn } from "@/lib/utils";

const POLICIES: { value: ImportPolicy; label: string; hint: string }[] = [
  { value: "error", label: "fail if it exists", hint: "refuse when a project with the same name exists" },
  { value: "rename", label: "rename", hint: "import as <name>-imported-<timestamp> when the name is taken" },
  { value: "overwrite", label: "overwrite", hint: "delete the existing project first — destructive" },
];

// ImportArchiveCard uploads a tarball from `kuso project export-archive`
// (or the project settings export) and recreates the project here.
export function ImportArchiveCard() {
  const router = useRouter();
  const qc = useQueryClient();
  const inputRef = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [policy, setPolicy] = useState<ImportPolicy>("error");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirmOverwrite, setConfirmOverwrite] = useState(false);

  const run = async () => {
    if (!file) return;
    setBusy(true);
    setError(null);
    try {
      const res = await importProjectArchive(file, policy);
      await Promise.all([
        qc.invalidateQueries({ queryKey: projectsQueryKey }),
        qc.invalidateQueries({ queryKey: projectsSummaryQueryKey }),
      ]);
      const summary = `${res.services} services, ${res.addons} addons, ${res.secrets} secrets`;
      if (res.warnings?.length) {
        toast.warning(`Imported ${res.project} (${summary}) with warnings`, {
          description: res.warnings.join("\n"),
          duration: Infinity,
          closeButton: true,
        });
      } else {
        toast.success(`Imported ${res.project} (${summary})`);
      }
      router.push(`/projects/${encodeURIComponent(res.project)}`);
    } catch (e) {
      setError(friendlyApiError(e, "Import failed"));
    } finally {
      setBusy(false);
      setConfirmOverwrite(false);
    }
  };

  const onImport = () => {
    if (policy === "overwrite") setConfirmOverwrite(true);
    else void run();
  };

  return (
    <section className="mb-4 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-4 py-3">
      <div className="flex items-center gap-3">
        <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--accent)]">
          <Archive className="h-4 w-4" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-[13px] font-medium text-[var(--text-primary)]">Import a project archive</span>
          <span className="block text-[12px] text-[var(--text-secondary)]">
            Recreate a project from an export tarball (specs and secrets; addon data isn&apos;t included).
          </span>
        </span>
      </div>
      <div className="mt-3 space-y-2">
        <input
          ref={inputRef}
          type="file"
          accept=".tar.gz,.tgz,application/gzip"
          onChange={(e) => {
            setFile(e.target.files?.[0] ?? null);
            setError(null);
          }}
          aria-label="Project archive"
          className="block w-full text-[12px] text-[var(--text-secondary)] file:mr-3 file:rounded-md file:border file:border-[var(--border-subtle)] file:bg-[var(--bg-tertiary)] file:px-2 file:py-1 file:text-[12px] file:text-[var(--text-primary)]"
        />
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">if the name exists</span>
          <div role="group" aria-label="Name conflict policy" className="inline-flex items-center gap-0.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
            {POLICIES.map((p) => (
              <button
                key={p.value}
                type="button"
                title={p.hint}
                aria-pressed={policy === p.value}
                onClick={() => setPolicy(p.value)}
                className={cn(
                  "rounded px-1.5 py-1 font-mono text-[10px] whitespace-nowrap transition-colors",
                  policy === p.value
                    ? p.value === "overwrite"
                      ? "bg-[var(--error-subtle)] text-[var(--error)]"
                      : "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                    : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
                )}
              >
                {p.label}
              </button>
            ))}
          </div>
          <Button type="button" size="sm" variant="outline" className="ml-auto" onClick={onImport} disabled={!file || busy}>
            {busy ? "Importing…" : "Import"}
          </Button>
        </div>
        <p className="text-[10px] text-[var(--text-tertiary)]">{POLICIES.find((p) => p.value === policy)?.hint}</p>
        {error && (
          <p role="alert" className="text-[11px] text-[var(--error)]">
            {error}
          </p>
        )}
      </div>
      <ConfirmDialog
        open={confirmOverwrite}
        title="Overwrite the existing project?"
        body={
          <>
            If a project with the archive&apos;s name exists, it is deleted first — its services,
            environments, addons and secrets — and then recreated from{" "}
            <span className="font-mono">{file?.name}</span>.
          </>
        }
        typeToConfirm="overwrite"
        confirmLabel="Overwrite and import"
        pending={busy}
        onConfirm={() => void run()}
        onCancel={() => setConfirmOverwrite(false)}
      />
    </section>
  );
}
