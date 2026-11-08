"use client";

import { Select } from "@/components/ui/select";
import { useEffect, useState } from "react";
import { Segmented } from "@/components/ui/segmented";
import { useQuery, useMutation } from "@tanstack/react-query";
import { Play } from "lucide-react";
import {
  listSQLTables,
  listSQLDatabases,
  runSQL,
  repairAddonPassword,
} from "@/features/projects";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { TableGrid } from "./data/TableGrid";

export function SQLTab({ project, addon }: { project: string; addon: string }) {
  // Two sub-modes: "browse" (structured table grid, default) and "sql" (the
  // raw read-only query runner). The table list on the left drives both —
  // clicking a table selects it for the grid in browse mode, or seeds a
  // SELECT in sql mode.
  const [mode, setMode] = useState<"browse" | "sql">("browse");
  const [selected, setSelected] = useState<{ schema: string; name: string } | null>(null);
  // Logical-database picker. A postgres addon is a SERVER that can hold
  // many databases (multi-tenant platforms create one per tenant); ""
  // means the addon's default DB. Non-postgres addons return an error or
  // a single entry — the picker hides itself below 2 databases.
  const [database, setDatabase] = useState("");
  const databases = useQuery({
    queryKey: ["addons", project, addon, "sql", "databases"],
    queryFn: () => listSQLDatabases(project, addon),
    staleTime: 60_000,
    retry: 1,
  });
  const tables = useQuery({
    queryKey: ["addons", project, addon, database, "sql", "tables"],
    queryFn: () => listSQLTables(project, addon, database || undefined),
    staleTime: 30_000,
    retry: 2,
    retryDelay: (i) => 1000 * (i + 1),
  });
  // Open the first table on load (and after a database switch) so the
  // tab lands on data, not an empty pane.
  useEffect(() => {
    if (selected || !tables.data || tables.data.length === 0) return;
    const first = tables.data.find((t) => t.schema === "public") ?? tables.data[0];
    setSelected({ schema: first.schema, name: first.name });
  }, [tables.data, selected]);
  const [query, setQuery] = useState("SELECT 1");
  const [resp, setResp] = useState<{
    columns: string[];
    rows: string[][];
    truncated: boolean;
    elapsed: string;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const run = useMutation({
    mutationFn: (q: string) => runSQL(project, addon, q, 100, database || undefined),
    onSuccess: (data) => {
      setResp(data);
      setError(null);
    },
    onError: (e) => {
      setError(e instanceof Error ? e.message : "Query failed");
      setResp(null);
    },
  });
  const repair = useMutation({
    mutationFn: () => repairAddonPassword(project, addon),
    onSuccess: () => {
      toast.success("Password resynced. Retrying…");
      tables.refetch();
      setError(null);
    },
    onError: (e) => toastError(e, "Repair failed"),
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
    setSelected({ schema, name });
    if (mode === "sql") {
      const safe = schema === "public" ? `"${name}"` : `"${schema}"."${name}"`;
      setQuery(`SELECT * FROM ${safe} LIMIT 100`);
    }
  };

  return (
    // minmax(0, 1fr) on the right column is load-bearing: a plain `1fr`
    // grid track expands to fit its contents, so a SELECT * on a wide
    // table (50+ columns) blows past the overlay width and turns the
    // whole tab into a horizontal-scroll mess. With minmax(0, …) the
    // track can shrink below its natural content width, which lets the
    // inner `overflow-auto` actually scroll the table independently.
    <div className="grid h-full min-w-0 grid-cols-1 grid-rows-[auto_minmax(0,1fr)] gap-0 md:grid-cols-[200px_minmax(0,1fr)] md:grid-rows-1">
      <aside className="max-h-48 overflow-y-auto border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-3 md:max-h-none md:border-r md:border-b-0">
        {(databases.data ?? []).length > 1 && (
          <>
            <h4 className="mb-1 text-xs font-medium text-[var(--text-tertiary)]">
              Database
            </h4>
            <Select
              size="sm"
              value={database}
              onChange={(e) => {
                setDatabase(e.target.value);
                setSelected(null);
                setResp(null);
                setError(null);
              }}
              className="font-mono"
              wrapperClassName="mb-3"
              aria-label="Database"
            >
              <option value="">(default)</option>
              {databases.data!.map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </Select>
          </>
        )}
        <h4 className="mb-2 text-xs font-medium text-[var(--text-tertiary)]">
          Tables
        </h4>
        {tables.isPending ? (
          <Skeleton className="h-32 w-full" />
        ) : tables.isError ? (
          <div className="space-y-2 text-xs">
            <p className="text-[var(--warning)]">
              {tables.error instanceof Error ? tables.error.message : "Couldn't load tables"}
            </p>
            {showRepair ? (
              <>
                <p className="text-[var(--text-tertiary)]">
                  The stored password no longer matches the one inside the
                  database. Repair resets the database user to the stored password.
                </p>
                <Button size="xs" variant="secondary" onClick={() => repair.mutate()} loading={repair.isPending}>
                  Repair password
                </Button>
              </>
            ) : (
              <>
                <p className="text-[var(--text-tertiary)]">
                  Postgres may still be starting. Click to retry.
                </p>
                <Button size="xs" variant="secondary" onClick={() => tables.refetch()}>
                  Retry
                </Button>
              </>
            )}
          </div>
        ) : (tables.data ?? []).length === 0 ? (
          <p className="text-xs text-[var(--text-tertiary)]">No user tables yet.</p>
        ) : (
          <ul className="space-y-0.5">
            {tables.data!.map((t) => (
              <li key={`${t.schema}.${t.name}`}>
                <button
                  type="button"
                  onClick={() => pickTable(t.schema, t.name)}
                  className={`block w-full truncate rounded px-2 py-1 text-left font-mono text-xs hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] ${
                    selected?.schema === t.schema && selected?.name === t.name
                      ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                      : "text-[var(--text-secondary)]"
                  }`}
                  title={`${t.schema}.${t.name}`}
                >
                  {t.schema === "public" ? t.name : `${t.schema}.${t.name}`}
                </button>
              </li>
            ))}
          </ul>
        )}
      </aside>

      <section className="flex min-h-0 min-w-0 flex-col">
        {/* Browse / SQL mode toggle */}
        <div className="flex items-center gap-3 border-b border-[var(--border-subtle)] px-3 py-2">
          <Segmented
            size="sm"
            label="Data view"
            value={mode}
            onChange={(m) => {
              setMode(m);
              if (m === "sql" && selected && query === "SELECT 1") {
                const safe = selected.schema === "public" ? `"${selected.name}"` : `"${selected.schema}"."${selected.name}"`;
                setQuery(`SELECT * FROM ${safe} LIMIT 100`);
              }
            }}
            options={[
              { value: "browse", label: "Browse", title: "Browse and edit rows" },
              { value: "sql", label: "Query", title: "Run read-only SQL" },
            ]}
          />
          {mode === "browse" && selected && (
            <span className="min-w-0 truncate font-mono text-xs text-[var(--text-secondary)]">
              {selected.schema === "public" ? selected.name : `${selected.schema}.${selected.name}`}
            </span>
          )}
        </div>

        {mode === "browse" ? (
          selected ? (
            <TableGrid
              project={project}
              addon={addon}
              database={database || undefined}
              schema={selected.schema}
              table={selected.name}
            />
          ) : (
            <div className="flex flex-1 items-center justify-center">
              <p className="px-4 text-center text-xs text-[var(--text-tertiary)]">
                Pick a table to browse and edit its data.
              </p>
            </div>
          )
        ) : (
          <>
            <div className="border-b border-[var(--border-subtle)] p-3">
              <textarea
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                spellCheck={false}
                rows={4}
                placeholder="SELECT * FROM users LIMIT 100"
                className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-xs text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
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
                <span className="text-xs text-[var(--text-tertiary)]">
                  Read-only · 5s timeout · up to 100 rows · ⌘/Ctrl + ↵ to run. Edit rows in Browse.
                </span>
                <Button size="sm" onClick={() => run.mutate(query)} disabled={run.isPending}>
                  <Play className="h-3 w-3" />
                  {run.isPending ? "Running…" : "Run"}
                </Button>
              </div>
            </div>
            <div className="min-h-0 flex-1 overflow-auto p-3">
              {error ? (
                <pre className="whitespace-pre-wrap rounded-md border border-[var(--error)]/30 bg-[var(--error-subtle)] p-3 font-mono text-xs text-[var(--error)]">
                  {error}
                </pre>
              ) : resp ? (
                <SQLResults resp={resp} />
              ) : (
                <p className="text-xs text-[var(--text-tertiary)]">
                  Write a query and press Run to see results here.
                </p>
              )}
            </div>
          </>
        )}
      </section>
    </div>
  );
}

export type SQLCellDisplay =
  | { kind: "null" }
  | { kind: "empty" }
  | { kind: "ambiguous" }
  | { kind: "text"; text: string };

// sqlCellDisplay decides how a result cell renders. '' is a real value
// and must never be shown as NULL; when the server doesn't say which
// cells are NULL, an empty cell is labelled as ambiguous instead.
export function sqlCellDisplay(cell: string, isNull: boolean | undefined): SQLCellDisplay {
  if (isNull) return { kind: "null" };
  if (cell !== "") return { kind: "text", text: cell.length > 200 ? cell.slice(0, 200) + "…" : cell };
  return isNull === false ? { kind: "empty" } : { kind: "ambiguous" };
}

function SQLCell({ cell }: { cell: SQLCellDisplay }) {
  switch (cell.kind) {
    case "null":
      return <span className="text-[var(--text-tertiary)]/60 italic">null</span>;
    case "empty":
      return <span className="text-[var(--text-tertiary)]/60">&apos;&apos;</span>;
    case "ambiguous":
      return (
        <span
          className="text-[var(--text-tertiary)]/60"
          title="Empty string or NULL: this runner can't tell them apart yet. Use Browse, or IS NULL in the query."
        >
          ∅
        </span>
      );
    default:
      return <>{cell.text}</>;
  }
}

function SQLResults({
  resp,
}: {
  // nulls arrives once the server reports it (like the Browse grid's
  // data.nulls); until then NULL and '' are indistinguishable here.
  resp: { columns: string[]; rows: string[][]; nulls?: boolean[][]; truncated: boolean; elapsed: string };
}) {
  if (resp.columns.length === 0) {
    return (
      <p className="text-xs text-[var(--text-tertiary)]">
        The query returned no columns ({resp.elapsed}).
      </p>
    );
  }
  return (
    <div className="overflow-auto rounded-md border border-[var(--border-subtle)]">
      <table className="w-full text-left font-mono text-xs">
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
                  <SQLCell cell={sqlCellDisplay(cell, resp.nulls?.[i]?.[j])} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <footer className="flex items-center justify-between border-t border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1 text-xs text-[var(--text-tertiary)]">
        <span>
          {resp.rows.length} row{resp.rows.length === 1 ? "" : "s"}
          {resp.truncated && (
            <span className="ml-2 text-[var(--text-secondary)]">· capped at 100</span>
          )}
        </span>
        <span>{resp.elapsed}</span>
      </footer>
    </div>
  );
}
