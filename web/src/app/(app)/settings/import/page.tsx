"use client";

import { useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ArrowDown, Database, Globe, Package, CheckCircle2, AlertTriangle } from "lucide-react";
import { useCan, Perms } from "@/features/auth";
import { toast } from "sonner";

// Coolify import wizard — preview + commit.
//
// Step 1: collect the Coolify URL + a read-only API token, run an
// inventory snapshot via /api/import/coolify/preview. The classifier
// verdict per resource gates which rows can be ticked (skip-classified
// rows are non-selectable; migrate-classified default to selected).
//
// Step 2: confirm — the server re-snapshots from the same credentials
// (server is the source of truth for verdicts, the client can't smuggle
// skip-classified rows past it) and provisions kuso projects, services,
// and addons. The response carries per-row reasons for anything that
// got skipped or errored so the user sees exactly what happened.
//
// Why server re-snapshot on commit: a client that round-trips the
// verdict list could otherwise tamper with it to escalate the import
// scope. Keeping classify→commit hermetic on the server makes the
// admin gate meaningful.

// Wire shape mirrors coolify.Item — only the fields we render. The
// classifier verdict comes wrapped in a sub-object; we flatten the
// action onto the row for ergonomics.
interface PreviewItem {
  uuid: string;
  name: string;
  projectName: string;
  envName: string;
  verdict: { kind: string; action: string; reason: string };
  // One of app/service/database is set so we can show the kind chip.
  app?: { uuid: string; name: string };
  service?: { uuid: string; name: string };
  database?: { uuid: string; name: string };
}

function itemKind(it: PreviewItem): string {
  if (it.app) return "application";
  if (it.database) return "database";
  if (it.service) return "service";
  return "unknown";
}

interface CommitDetail {
  kind: string;
  name: string;
  reason: string;
}

interface CommitResponse {
  projectsCreated: number;
  servicesCreated: number;
  addonsCreated: number;
  envVarsCreated: number;
  skipped: CommitDetail[];
  errors: CommitDetail[];
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
  // Picked rows are tracked outside the preview response so a re-run
  // of preview doesn't blow away the user's selection — they can
  // re-snapshot if Coolify changed mid-wizard without losing context.
  const [picked, setPicked] = useState<Record<string, boolean>>({});
  const [commitResult, setCommitResult] = useState<CommitResponse | null>(null);

  const preview = useMutation<PreviewResponse, Error>({
    mutationFn: () =>
      api<PreviewResponse>("/api/import/coolify/preview", {
        method: "POST",
        body: { baseUrl, token },
      }),
    onSuccess: (data) => {
      // Default-select every migrate-classified row so the user
      // only has to uncheck things they want to skip.
      const next: Record<string, boolean> = {};
      for (const it of data.items) {
        if (it.verdict?.action === "migrate" && it.uuid) {
          next[it.uuid] = true;
        }
      }
      setPicked(next);
      setCommitResult(null);
    },
  });

  const commit = useMutation<CommitResponse, Error, string[]>({
    mutationFn: (uuids) =>
      api<CommitResponse>("/api/import/coolify/commit", {
        method: "POST",
        body: { baseUrl, token, uuids },
      }),
    onSuccess: (data) => {
      setCommitResult(data);
      const total = data.projectsCreated + data.servicesCreated + data.addonsCreated;
      if (data.errors.length === 0) {
        toast.success(`Imported ${total} resources from Coolify`);
      } else {
        toast.warning(`Imported ${total} with ${data.errors.length} error(s) — see details below`);
      }
    },
    onError: (err) => {
      toast.error(err.message);
    },
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

      {preview.data && (
        <PreviewTable
          data={preview.data}
          picked={picked}
          onTogglePick={(uuid, on) => setPicked((p) => ({ ...p, [uuid]: on }))}
          onCommit={() => {
            const uuids = Object.entries(picked)
              .filter(([, on]) => on)
              .map(([uuid]) => uuid);
            commit.mutate(uuids);
          }}
          commitPending={commit.isPending}
          commitError={commit.error?.message}
          commitResult={commitResult}
        />
      )}
    </div>
  );
}

function PreviewTable({
  data,
  picked,
  onTogglePick,
  onCommit,
  commitPending,
  commitError,
  commitResult,
}: {
  data: PreviewResponse;
  picked: Record<string, boolean>;
  onTogglePick: (uuid: string, on: boolean) => void;
  onCommit: () => void;
  commitPending: boolean;
  commitError?: string;
  commitResult: CommitResponse | null;
}) {
  const stats = data.stats;
  const pickedCount = useMemo(
    () => Object.values(picked).filter(Boolean).length,
    [picked]
  );
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
              <Th>
                <span className="sr-only">Select</span>
              </Th>
              <Th>Kind</Th>
              <Th>Project · Env</Th>
              <Th>Name</Th>
              <Th>Verdict</Th>
              <Th>Reason / Suggested</Th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--border-subtle)]">
            {data.items.map((it) => {
              const action = it.verdict?.action ?? "skip";
              const reason = it.verdict?.reason ?? "";
              const importable = action === "migrate" && !!it.uuid;
              return (
                <tr key={it.uuid || it.name} className="hover:bg-[var(--bg-secondary)]/40">
                  <Td>
                    <input
                      type="checkbox"
                      disabled={!importable || commitPending}
                      checked={importable ? !!picked[it.uuid] : false}
                      onChange={(e) => onTogglePick(it.uuid, e.target.checked)}
                      aria-label={`include ${it.name}`}
                    />
                  </Td>
                  <Td className="font-mono text-[11px]">{itemKind(it)}</Td>
                  <Td className="font-mono text-[11px] text-[var(--text-secondary)]">
                    {it.projectName}
                    {it.envName ? <span className="text-[var(--text-tertiary)]"> · {it.envName}</span> : null}
                  </Td>
                  <Td className="font-mono text-[11px]">{it.name}</Td>
                  <Td>
                    <span
                      className={
                        action === "migrate"
                          ? "rounded bg-emerald-500/10 px-1.5 py-0.5 font-mono text-[10px] text-emerald-300"
                          : action === "flag"
                            ? "rounded bg-amber-500/10 px-1.5 py-0.5 font-mono text-[10px] text-amber-300"
                            : "rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)]"
                      }
                    >
                      {action}
                    </span>
                  </Td>
                  <Td className="text-[var(--text-secondary)]">{reason}</Td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-3">
        <Button onClick={onCommit} disabled={pickedCount === 0 || commitPending} size="sm">
          {commitPending
            ? `Importing ${pickedCount}…`
            : `Import ${pickedCount} resource${pickedCount === 1 ? "" : "s"}`}
        </Button>
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
          The server re-runs the classifier on commit; only migrate-verdict rows are sent.
        </span>
      </div>

      {commitError && (
        <p className="mt-3 rounded-md border border-red-500/40 bg-red-500/5 p-2 text-[12px] text-red-300">
          {commitError}
        </p>
      )}

      {commitResult && <CommitResultPanel result={commitResult} />}
    </section>
  );
}

function CommitResultPanel({ result }: { result: CommitResponse }) {
  const total = result.projectsCreated + result.servicesCreated + result.addonsCreated;
  const ok = result.errors.length === 0;
  return (
    <section
      className={
        ok
          ? "mt-4 rounded-md border border-emerald-500/40 bg-emerald-500/5 p-4"
          : "mt-4 rounded-md border border-amber-500/40 bg-amber-500/5 p-4"
      }
    >
      <header className="flex items-start gap-2">
        {ok ? (
          <CheckCircle2 className="mt-0.5 h-4 w-4 text-emerald-400" />
        ) : (
          <AlertTriangle className="mt-0.5 h-4 w-4 text-amber-400" />
        )}
        <div>
          <div className="text-[12px] font-semibold">
            Imported {total} resource{total === 1 ? "" : "s"}
          </div>
          <div className="font-mono text-[11px] text-[var(--text-secondary)]">
            projects {result.projectsCreated} · services {result.servicesCreated} · addons {result.addonsCreated} · env vars {result.envVarsCreated}
          </div>
        </div>
      </header>
      {(result.skipped.length > 0 || result.errors.length > 0) && (
        <ul className="mt-3 space-y-1 font-mono text-[11px]">
          {result.errors.map((d, i) => (
            <li key={`e-${i}`} className="text-red-300">
              ✗ {d.kind} {d.name} — {d.reason}
            </li>
          ))}
          {result.skipped.map((d, i) => (
            <li key={`s-${i}`} className="text-[var(--text-tertiary)]">
              · {d.kind} {d.name} — {d.reason}
            </li>
          ))}
        </ul>
      )}
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
