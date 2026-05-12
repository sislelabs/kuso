"use client";

import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ArrowDown, Database, Globe, Package } from "lucide-react";
import { useCan, Perms } from "@/features/auth";

// Coolify import wizard — preview half.
//
// This page collects the Coolify URL + a read-only API token, then
// runs an inventory snapshot via the new /api/import/coolify/preview
// endpoint. We render the classifier verdict per resource. The
// "commit" half (actually create kuso projects + services + addons
// from the picked rows) is a follow-up endpoint that doesn't yet
// exist on the server.
//
// Why preview-first: a Coolify instance with 50 apps and 12 dbs
// shouldn't get half-imported because the wizard's first network
// blip stomped the work midway. A user reviewing the table can
// uncheck rows they don't want, see exactly which addons map to
// what kuso shape, and commit only after confirmation.

interface PreviewItem {
  kind: string; // application | database | service
  name: string;
  projectName: string;
  envName: string;
  verdict: string; // migrate | flag | skip
  reason?: string;
  suggested?: string;
}

interface PreviewStats {
  numApps: number;
  numDBs: number;
  numServices: number;
  numSkipped: number;
  numMigrate: number;
  numFlag: number;
}

interface PreviewResponse {
  coolifyVersion: string;
  stats: PreviewStats;
  items: PreviewItem[];
}

export default function ImportPage() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const [baseUrl, setBaseUrl] = useState("");
  const [token, setToken] = useState("");
  const preview = useMutation<PreviewResponse, Error>({
    mutationFn: () =>
      api<PreviewResponse>("/api/import/coolify/preview", {
        method: "POST",
        body: { baseUrl, token },
      }),
  });

  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-2xl p-6 lg:p-8">
        <p className="text-sm text-[var(--text-secondary)]">
          The Coolify import is admin-only. Ask a team admin to run it for you.
        </p>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-4xl p-6 lg:p-8">
      <header className="mb-6 flex items-start gap-3">
        <ArrowDown className="mt-1 h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">Import from Coolify</h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Connect to a Coolify v4 instance, preview which resources can be
            imported, then commit the migration. Preview is read-only — no
            writes to either Coolify or kuso until you confirm.
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
            Coolify URL
          </span>
          <Input
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            placeholder="https://coolify.example.com"
            className="mt-1 h-8 font-mono text-[13px]"
            required
          />
        </label>
        <label className="block">
          <span className="block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            API token (read-only is fine)
          </span>
          <Input
            value={token}
            onChange={(e) => setToken(e.target.value)}
            type="password"
            className="mt-1 h-8 font-mono text-[13px]"
            required
          />
        </label>
        <div className="flex justify-end">
          <Button type="submit" size="sm" disabled={preview.isPending}>
            {preview.isPending ? "Snapshotting…" : "Preview import"}
          </Button>
        </div>
        {preview.isError && (
          <p className="rounded-md border border-red-500/40 bg-red-500/5 p-2 text-[12px] text-red-300">
            {preview.error.message}
          </p>
        )}
      </form>

      {preview.isPending && (
        <div className="mt-4 space-y-2">
          {[...Array(5)].map((_, i) => (
            <Skeleton key={i} className="h-9 w-full" />
          ))}
        </div>
      )}

      {preview.data && <PreviewTable data={preview.data} />}
    </div>
  );
}

function PreviewTable({ data }: { data: PreviewResponse }) {
  const stats = data.stats;
  return (
    <section className="mt-6">
      <header className="mb-3 flex flex-wrap items-center gap-3 text-[12px]">
        <StatChip icon={<Package className="h-3 w-3" />} label="apps" value={stats.numApps} />
        <StatChip icon={<Database className="h-3 w-3" />} label="dbs" value={stats.numDBs} />
        <StatChip icon={<Globe className="h-3 w-3" />} label="services" value={stats.numServices} />
        <span className="ml-auto font-mono text-[10px] text-[var(--text-tertiary)]">
          Coolify {data.coolifyVersion || "v4"} · migrate {stats.numMigrate} · flag {stats.numFlag} · skip {stats.numSkipped}
        </span>
      </header>

      <div className="overflow-hidden rounded-md border border-[var(--border-subtle)]">
        <table className="w-full text-[12px]">
          <thead className="bg-[var(--bg-secondary)] text-[var(--text-tertiary)]">
            <tr>
              <Th>Kind</Th>
              <Th>Project · Env</Th>
              <Th>Name</Th>
              <Th>Verdict</Th>
              <Th>Reason / Suggested</Th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--border-subtle)]">
            {data.items.map((it, i) => (
              <tr key={i} className="hover:bg-[var(--bg-secondary)]/40">
                <Td className="font-mono text-[11px]">{it.kind}</Td>
                <Td className="font-mono text-[11px] text-[var(--text-secondary)]">
                  {it.projectName}
                  {it.envName ? <span className="text-[var(--text-tertiary)]"> · {it.envName}</span> : null}
                </Td>
                <Td className="font-mono text-[11px]">{it.name}</Td>
                <Td>
                  <span
                    className={
                      it.verdict === "migrate"
                        ? "rounded bg-emerald-500/10 px-1.5 py-0.5 font-mono text-[10px] text-emerald-300"
                        : it.verdict === "flag"
                          ? "rounded bg-amber-500/10 px-1.5 py-0.5 font-mono text-[10px] text-amber-300"
                          : "rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)]"
                    }
                  >
                    {it.verdict}
                  </span>
                </Td>
                <Td className="text-[var(--text-secondary)]">
                  {it.reason || it.suggested || ""}
                </Td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <p className="mt-3 rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-[12px] text-amber-200">
        <strong className="font-semibold">Preview only.</strong> The commit step
        (actually create kuso projects + services from the green rows) is a
        follow-up endpoint. For now, run{" "}
        <span className="font-mono">kuso migrate coolify --token=... --baseUrl=...</span>{" "}
        from the CLI to perform the migration based on this same classifier.
      </p>
    </section>
  );
}

function StatChip({ icon, label, value }: { icon: React.ReactNode; label: string; value: number }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-2 py-1 font-mono text-[11px]">
      {icon}
      <span className="text-[var(--text-primary)]">{value}</span>
      <span className="text-[var(--text-tertiary)]">{label}</span>
    </span>
  );
}

function Th({ children }: { children: React.ReactNode }) {
  return <th className="px-3 py-2 text-left font-mono text-[10px] uppercase tracking-widest font-normal">{children}</th>;
}

function Td({ children, className }: { children: React.ReactNode; className?: string }) {
  return <td className={`px-3 py-2 ${className ?? ""}`}>{children}</td>;
}
