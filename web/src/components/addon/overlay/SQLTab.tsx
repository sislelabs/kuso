"use client";

import { useState } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { Play } from "lucide-react";
import {
  listSQLTables,
  runSQL,
  repairAddonPassword,
} from "@/features/projects";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";

export function SQLTab({ project, addon }: { project: string; addon: string }) {
  const tables = useQuery({
    queryKey: ["addons", project, addon, "sql", "tables"],
    queryFn: () => listSQLTables(project, addon),
    staleTime: 30_000,
    retry: 2,
    retryDelay: (i) => 1000 * (i + 1),
  });
  const [query, setQuery] = useState("SELECT 1");
  const [resp, setResp] = useState<{
    columns: string[];
    rows: string[][];
    truncated: boolean;
    elapsed: string;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const run = useMutation({
    mutationFn: (q: string) => runSQL(project, addon, q, 100),
    onSuccess: (data) => {
      setResp(data);
      setError(null);
    },
    onError: (e) => {
      setError(e instanceof Error ? e.message : "query failed");
      setResp(null);
    },
  });
  const repair = useMutation({
    mutationFn: () => repairAddonPassword(project, addon),
    onSuccess: () => {
      toast.success("password resynced — retrying");
      tables.refetch();
      setError(null);
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "repair failed"),
  });

  // The drift bug surfaces as Postgres SQLSTATE 28P01. Detect both
  // the explicit code and the human-readable form.
  const looksLikePasswordDrift = (msg: string) =>
    /password authentication failed/i.test(msg) || /28P01/.test(msg);
  const tablesErrMsg = tables.error instanceof Error ? tables.error.message : "";
  const queryErrMsg = error ?? "";
  const showRepair =
    looksLikePasswordDrift(tablesErrMsg) || looksLikePasswordDrift(queryErrMsg);

  const pickTable = (schema: string, name: string) => {
    const safe = schema === "public" ? `"${name}"` : `"${schema}"."${name}"`;
    setQuery(`SELECT * FROM ${safe} LIMIT 100`);
  };

  return (
    <div className="grid h-full grid-cols-[200px_1fr] gap-0">
      <aside className="overflow-y-auto border-r border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-3">
        <h4 className="mb-2 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          tables
        </h4>
        {tables.isPending ? (
          <Skeleton className="h-32 w-full" />
        ) : tables.isError ? (
          <div className="space-y-2 text-[10px]">
            <p className="text-amber-400">
              {tables.error instanceof Error ? tables.error.message : "load failed"}
            </p>
            {showRepair ? (
              <>
                <p className="text-[var(--text-tertiary)]">
                  Looks like password drift — the chart&apos;s conn secret no longer
                  matches the password baked into pgdata. Repair resyncs the user via
                  ALTER USER inside the pod.
                </p>
                <button
                  type="button"
                  onClick={() => repair.mutate()}
                  disabled={repair.isPending}
                  className="rounded border border-amber-500/40 bg-amber-500/10 px-2 py-1 font-mono text-[10px] text-amber-200 hover:bg-amber-500/20 disabled:opacity-50"
                >
                  {repair.isPending ? "Repairing…" : "Repair password"}
                </button>
              </>
            ) : (
              <>
                <p className="text-[var(--text-tertiary)]">
                  Postgres may still be starting. Click to retry.
                </p>
                <button
                  type="button"
                  onClick={() => tables.refetch()}
                  className="rounded border border-[var(--border-subtle)] px-2 py-1 font-mono text-[10px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
                >
                  Retry
                </button>
              </>
            )}
          </div>
        ) : (tables.data ?? []).length === 0 ? (
          <p className="text-[10px] text-[var(--text-tertiary)]">no user tables</p>
        ) : (
          <ul className="space-y-0.5">
            {tables.data!.map((t) => (
              <li key={`${t.schema}.${t.name}`}>
                <button
                  type="button"
                  onClick={() => pickTable(t.schema, t.name)}
                  className="block w-full truncate rounded px-2 py-1 text-left font-mono text-[11px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                  title={`${t.schema}.${t.name}`}
                >
                  {t.schema === "public" ? t.name : `${t.schema}.${t.name}`}
                </button>
              </li>
            ))}
          </ul>
        )}
      </aside>

      <section className="flex min-h-0 flex-col">
        <div className="border-b border-[var(--border-subtle)] p-3">
          <textarea
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            spellCheck={false}
            rows={4}
            placeholder="SELECT * FROM users LIMIT 100"
            className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-[12px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
            onKeyDown={(e) => {
              // ⌘/Ctrl+Enter runs the query — same shortcut every other
              // SQL UI uses, so muscle memory carries over from psql,
              // DBeaver, etc.
              if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
                e.preventDefault();
                run.mutate(query);
              }
            }}
          />
          <div className="mt-2 flex items-center justify-between">
            <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
              read-only · 5s timeout · max 100 rows · ⌘/Ctrl + ↵ to run
            </span>
            <Button size="sm" onClick={() => run.mutate(query)} disabled={run.isPending}>
              <Play className="h-3 w-3" />
              {run.isPending ? "Running…" : "Run"}
            </Button>
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-auto p-3">
          {error ? (
            <pre className="whitespace-pre-wrap rounded-md border border-red-500/30 bg-red-500/5 p-3 font-mono text-[11px] text-red-400">
              {error}
            </pre>
          ) : resp ? (
            <SQLResults resp={resp} />
          ) : (
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
              no results yet — write a query and hit Run.
            </p>
          )}
        </div>
      </section>
    </div>
  );
}

function SQLResults({
  resp,
}: {
  resp: { columns: string[]; rows: string[][]; truncated: boolean; elapsed: string };
}) {
  if (resp.columns.length === 0) {
    return (
      <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
        query returned no columns ({resp.elapsed})
      </p>
    );
  }
  return (
    <div className="overflow-auto rounded-md border border-[var(--border-subtle)]">
      <table className="w-full text-left font-mono text-[11px]">
        <thead className="bg-[var(--bg-secondary)] text-[var(--text-tertiary)]">
          <tr>
            {resp.columns.map((c) => (
              <th
                key={c}
                className="border-b border-[var(--border-subtle)] px-2 py-1.5 font-medium"
              >
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {resp.rows.map((row, i) => (
            <tr
              key={i}
              className="border-b border-[var(--border-subtle)] last:border-b-0 hover:bg-[var(--bg-tertiary)]/30"
            >
              {row.map((cell, j) => (
                <td
                  key={j}
                  className="px-2 py-1 align-top text-[var(--text-secondary)]"
                >
                  {cell === "" ? (
                    <span className="text-[var(--text-tertiary)]/60 italic">null</span>
                  ) : cell.length > 200 ? (
                    cell.slice(0, 200) + "…"
                  ) : (
                    cell
                  )}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <footer className="flex items-center justify-between border-t border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1 font-mono text-[10px] text-[var(--text-tertiary)]">
        <span>
          {resp.rows.length} row{resp.rows.length === 1 ? "" : "s"}
          {resp.truncated && (
            <span className="ml-2 text-amber-400">· truncated at 100</span>
          )}
        </span>
        <span>{resp.elapsed}</span>
      </footer>
    </div>
  );
}
