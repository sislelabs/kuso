"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, Plus, Trash2, ArrowUp, ArrowDown } from "lucide-react";
import {
  getSQLColumns,
  getSQLRows,
  updateSQLRow,
  deleteSQLRow,
  type SQLColumn,
  type SQLCellValue,
} from "@/features/projects";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { CellEditor } from "./CellEditor";
import { InsertRowDialog } from "./InsertRowDialog";

const PAGE = 100;

// TableGrid renders the paginated, sortable, inline-editable data grid for a
// single table. Editing/insert/delete are gated on the table having a PK
// (server enforces it too); PK-less tables render read-only with a banner.
export function TableGrid({
  project,
  addon,
  schema,
  table,
  database,
}: {
  project: string;
  addon: string;
  schema: string;
  table: string;
  database?: string;
}) {
  const qc = useQueryClient();
  const [page, setPage] = useState(0);
  const [orderBy, setOrderBy] = useState<string>("");
  const [dir, setDir] = useState<"asc" | "desc">("asc");
  const [insertOpen, setInsertOpen] = useState(false);

  // Reset paging/sort when the selected table changes.
  const tableKey = `${schema}.${table}`;
  const [lastKey, setLastKey] = useState(tableKey);
  if (lastKey !== tableKey) {
    setLastKey(tableKey);
    setPage(0);
    setOrderBy("");
    setDir("asc");
  }

  const cols = useQuery({
    queryKey: ["addons", project, addon, database ?? "", "sql", "columns", schema, table],
    queryFn: () => getSQLColumns(project, addon, schema, table, database),
    staleTime: 60_000,
  });

  const rows = useQuery({
    queryKey: ["addons", project, addon, database ?? "", "sql", "rows", schema, table, page, orderBy, dir],
    queryFn: () =>
      getSQLRows(project, addon, {
        schema,
        table,
        database,
        limit: PAGE,
        offset: page * PAGE,
        orderBy: orderBy || undefined,
        dir: orderBy ? dir : undefined,
      }),
    staleTime: 10_000,
    placeholderData: (prev) => prev,
  });

  const editable = cols.data?.editable ?? false;
  const pk = cols.data?.primaryKey ?? [];
  const colByName = new Map<string, SQLColumn>((cols.data?.columns ?? []).map((c) => [c.name, c]));

  const invalidate = () =>
    qc.invalidateQueries({ queryKey: ["addons", project, addon, database ?? "", "sql", "rows", schema, table] });

  const del = useMutation({
    mutationFn: (pkVals: Record<string, SQLCellValue>) =>
      deleteSQLRow(project, addon, schema, table, pkVals, database),
    onSuccess: () => {
      toast.success("row deleted");
      invalidate();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "delete failed"),
  });

  const update = useMutation({
    mutationFn: (v: {
      pk: Record<string, SQLCellValue>;
      set: Record<string, SQLCellValue>;
    }) => updateSQLRow(project, addon, schema, table, v.pk, v.set, database),
    onSuccess: () => {
      toast.success("saved");
      invalidate();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "update failed"),
  });

  // Build the PK value map for a given displayed row. Throws if a PK column
  // isn't in the result set (shouldn't happen with SELECT *, but a silent
  // partial PK would produce an opaque server 422 — fail loud instead).
  const pkOf = (rowIdx: number): Record<string, SQLCellValue> => {
    const r = rows.data!;
    const map: Record<string, SQLCellValue> = {};
    for (const col of pk) {
      const ci = r.columns.indexOf(col);
      if (ci < 0) throw new Error(`primary-key column "${col}" missing from result`);
      map[col] = { value: r.rows[rowIdx][ci], isNull: r.nulls[rowIdx][ci] };
    }
    return map;
  };

  // Resolve a row's PK, toasting + returning null on the (shouldn't-happen)
  // missing-column case so callers never fire a malformed write.
  const safePkOf = (rowIdx: number): Record<string, SQLCellValue> | null => {
    try {
      return pkOf(rowIdx);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "could not resolve primary key");
      return null;
    }
  };

  const toggleSort = (col: string) => {
    if (orderBy !== col) {
      setOrderBy(col);
      setDir("asc");
    } else if (dir === "asc") {
      setDir("desc");
    } else {
      setOrderBy("");
    }
    setPage(0);
  };

  if (cols.isPending || (rows.isPending && !rows.data)) {
    return <Skeleton className="m-3 h-64" />;
  }
  if (rows.isError) {
    return (
      <pre className="m-3 whitespace-pre-wrap rounded-md border border-red-500/30 bg-red-500/5 p-3 font-mono text-[11px] text-red-400">
        {rows.error instanceof Error ? rows.error.message : "load failed"}
      </pre>
    );
  }

  const data = rows.data!;
  const total = data.total;
  const pages = Math.max(1, Math.ceil(total / PAGE));

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      {/* toolbar */}
      <div className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
        <div className="flex items-center gap-2 font-mono text-[11px] text-[var(--text-secondary)]">
          <span className="text-[var(--text-primary)]">
            {schema === "public" ? table : `${schema}.${table}`}
          </span>
          <span className="text-[var(--text-tertiary)]">· {total} rows</span>
          {!editable && (
            <span className="rounded bg-amber-500/10 px-1.5 py-0.5 text-[10px] text-amber-300">
              read-only (no primary key)
            </span>
          )}
        </div>
        {editable && (
          <button
            type="button"
            onClick={() => setInsertOpen(true)}
            className="flex items-center gap-1 rounded border border-[var(--border-subtle)] px-2 py-1 font-mono text-[10px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
          >
            <Plus className="h-3 w-3" /> insert row
          </button>
        )}
      </div>

      {/* grid */}
      <div className="min-h-0 flex-1 overflow-auto">
        <table className="w-full text-left font-mono text-[11px]">
          <thead className="sticky top-0 bg-[var(--bg-secondary)] text-[var(--text-tertiary)]">
            <tr>
              {editable && <th className="w-8 border-b border-[var(--border-subtle)] px-2 py-1.5" />}
              {data.columns.map((c) => (
                <th
                  key={c}
                  className="cursor-pointer border-b border-[var(--border-subtle)] px-2 py-1.5 font-medium hover:text-[var(--text-secondary)]"
                  onClick={() => toggleSort(c)}
                  title={colByName.get(c)?.dataType}
                >
                  <span className="inline-flex items-center gap-1">
                    {pk.includes(c) && <span className="text-amber-400/70">★</span>}
                    {c}
                    {orderBy === c &&
                      (dir === "asc" ? (
                        <ArrowUp className="h-3 w-3" />
                      ) : (
                        <ArrowDown className="h-3 w-3" />
                      ))}
                  </span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {data.rows.map((row, ri) => (
              <tr
                key={ri}
                className="group border-b border-[var(--border-subtle)] last:border-b-0 hover:bg-[var(--bg-tertiary)]/30"
              >
                {editable && (
                  <td className="px-2 py-1 align-top">
                    <button
                      type="button"
                      onClick={() => {
                        const k = safePkOf(ri);
                        if (k && confirm("Delete this row?")) del.mutate(k);
                      }}
                      className="opacity-0 transition-opacity group-hover:opacity-100"
                      title="delete row"
                    >
                      <Trash2 className="h-3 w-3 text-red-400/70 hover:text-red-400" />
                    </button>
                  </td>
                )}
                {row.map((cell, ci) => {
                  const colName = data.columns[ci];
                  const col = colByName.get(colName);
                  const isNull = data.nulls[ri][ci];
                  return (
                    <td key={ci} className="border-l border-[var(--border-subtle)]/40 px-0 py-0 align-top first:border-l-0">
                      {editable && col && !pk.includes(colName) ? (
                        <CellEditor
                          col={col}
                          value={cell}
                          isNull={isNull}
                          onSave={(v) => {
                            const k = safePkOf(ri);
                            if (k) update.mutate({ pk: k, set: { [colName]: v } });
                          }}
                        />
                      ) : (
                        <div className="px-2 py-1 text-[var(--text-secondary)]">
                          {isNull ? (
                            <span className="italic text-[var(--text-tertiary)]/60">null</span>
                          ) : cell.length > 200 ? (
                            cell.slice(0, 200) + "…"
                          ) : (
                            cell
                          )}
                        </div>
                      )}
                    </td>
                  );
                })}
              </tr>
            ))}
            {data.rows.length === 0 && (
              <tr>
                <td
                  colSpan={data.columns.length + (editable ? 1 : 0)}
                  className="px-3 py-6 text-center text-[var(--text-tertiary)]"
                >
                  no rows
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {/* pagination */}
      <footer className="flex items-center justify-between border-t border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-1.5 font-mono text-[10px] text-[var(--text-tertiary)]">
        <span>
          {total === 0 ? "0" : `${page * PAGE + 1}–${page * PAGE + data.rows.length}`} of {total}
          <span className="ml-2">· {data.elapsed}</span>
        </span>
        <div className="flex items-center gap-2">
          <button
            type="button"
            disabled={page === 0}
            onClick={() => setPage((p) => Math.max(0, p - 1))}
            className="disabled:opacity-30"
          >
            <ChevronLeft className="h-4 w-4" />
          </button>
          <span>
            {page + 1} / {pages}
          </span>
          <button
            type="button"
            disabled={page + 1 >= pages}
            onClick={() => setPage((p) => p + 1)}
            className="disabled:opacity-30"
          >
            <ChevronRight className="h-4 w-4" />
          </button>
        </div>
      </footer>

      {insertOpen && cols.data && (
        <InsertRowDialog
          project={project}
          addon={addon}
          database={database}
          schema={schema}
          table={table}
          columns={cols.data.columns}
          onClose={() => setInsertOpen(false)}
          onInserted={() => {
            setInsertOpen(false);
            invalidate();
          }}
        />
      )}
    </div>
  );
}
