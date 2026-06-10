"use client";

import { useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { X } from "lucide-react";
import { insertSQLRow, type SQLColumn, type SQLCellValue } from "@/features/projects";
import { Button } from "@/components/ui/button";
import { useMutation } from "@tanstack/react-query";
import { toast } from "sonner";
import { inputKindFor, validateCell, toCellValue } from "./cellInput";

// Per-field draft state. omit=true means "don't send this column" → the DB
// applies its default (or NULL). That's distinct from sending an explicit
// NULL, which a column with a non-null default needs to actually be nulled.
interface FieldState {
  raw: string;
  isNull: boolean;
  omit: boolean;
}

export function InsertRowDialog({
  project,
  addon,
  schema,
  table,
  columns,
  onClose,
  onInserted,
}: {
  project: string;
  addon: string;
  schema: string;
  table: string;
  columns: SQLColumn[];
  onClose: () => void;
  onInserted: () => void;
}) {
  const [fields, setFields] = useState<Record<string, FieldState>>(() => {
    const init: Record<string, FieldState> = {};
    for (const c of columns) {
      // Default to omitting columns that have a DB default (incl. serial
      // PKs) so the common case "just fill the real columns" works.
      init[c.name] = { raw: "", isNull: false, omit: !!c.default };
    }
    return init;
  });
  const [errs, setErrs] = useState<Record<string, string>>({});

  const set = (name: string, patch: Partial<FieldState>) =>
    setFields((f) => ({ ...f, [name]: { ...f[name], ...patch } }));

  const insert = useMutation({
    mutationFn: () => {
      const values: Record<string, SQLCellValue> = {};
      const nextErrs: Record<string, string> = {};
      for (const c of columns) {
        const fs = fields[c.name];
        if (fs.omit) continue;
        const e = validateCell(c, fs.raw, fs.isNull);
        if (e) nextErrs[c.name] = e;
        else values[c.name] = toCellValue(c, fs.raw, fs.isNull);
      }
      if (Object.keys(nextErrs).length) {
        setErrs(nextErrs);
        throw new Error("fix the highlighted fields");
      }
      setErrs({});
      return insertSQLRow(project, addon, schema, table, values);
    },
    onSuccess: () => {
      toast.success("row inserted");
      onInserted();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "insert failed"),
  });

  return (
    <AnimatePresence>
      <motion.div
        className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        exit={{ opacity: 0 }}
        onClick={onClose}
      >
        <motion.div
          className="flex max-h-[80vh] w-full max-w-lg flex-col overflow-hidden rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-primary)] shadow-2xl"
          initial={{ scale: 0.96, y: 8 }}
          animate={{ scale: 1, y: 0 }}
          exit={{ scale: 0.96, y: 8 }}
          onClick={(e) => e.stopPropagation()}
        >
          <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-3">
            <h3 className="font-mono text-sm text-[var(--text-primary)]">
              insert into {schema === "public" ? table : `${schema}.${table}`}
            </h3>
            <button type="button" onClick={onClose}>
              <X className="h-4 w-4 text-[var(--text-tertiary)]" />
            </button>
          </header>

          <div className="min-h-0 flex-1 space-y-2 overflow-y-auto p-4">
            {columns.map((c) => {
              const fs = fields[c.name];
              const kind = inputKindFor(c);
              return (
                <div key={c.name} className="grid grid-cols-[140px_1fr] items-start gap-2">
                  <label className="pt-1.5 font-mono text-[11px] text-[var(--text-secondary)]">
                    {c.name}
                    <span className="block text-[9px] text-[var(--text-tertiary)]">
                      {c.dataType}
                      {c.default ? " · has default" : c.nullable ? " · nullable" : " · required"}
                    </span>
                  </label>
                  <div className="space-y-0.5">
                    <div className="flex items-center gap-1">
                      <label className="flex items-center gap-1 font-mono text-[9px] text-[var(--text-tertiary)]">
                        <input
                          type="checkbox"
                          checked={fs.omit}
                          onChange={(e) => set(c.name, { omit: e.target.checked })}
                        />
                        default
                      </label>
                      {!fs.omit && c.nullable && (
                        <button
                          type="button"
                          onClick={() => set(c.name, { isNull: !fs.isNull })}
                          className={`rounded px-1 text-[9px] ${
                            fs.isNull
                              ? "bg-amber-500/20 text-amber-300"
                              : "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)]"
                          }`}
                        >
                          null
                        </button>
                      )}
                    </div>
                    {!fs.omit && !fs.isNull &&
                      (kind === "bool" || kind === "enum" ? (
                        <select
                          value={fs.raw}
                          onChange={(e) => set(c.name, { raw: e.target.value })}
                          className="w-full rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-1.5 py-1 font-mono text-[11px]"
                        >
                          <option value="">—</option>
                          {(kind === "bool" ? ["true", "false"] : c.enumValues ?? []).map((v) => (
                            <option key={v} value={v}>
                              {v}
                            </option>
                          ))}
                        </select>
                      ) : kind === "json" ? (
                        <textarea
                          value={fs.raw}
                          onChange={(e) => set(c.name, { raw: e.target.value })}
                          rows={2}
                          spellCheck={false}
                          placeholder="{}"
                          className="w-full resize-y rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-1.5 py-1 font-mono text-[11px]"
                        />
                      ) : (
                        <input
                          value={fs.raw}
                          onChange={(e) => set(c.name, { raw: e.target.value })}
                          inputMode={kind === "number" ? "decimal" : undefined}
                          className="w-full rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-1.5 py-1 font-mono text-[11px]"
                        />
                      ))}
                    {errs[c.name] && (
                      <span className="text-[9px] text-red-400">{errs[c.name]}</span>
                    )}
                  </div>
                </div>
              );
            })}
          </div>

          <footer className="flex justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
            <Button size="sm" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button size="sm" onClick={() => insert.mutate()} disabled={insert.isPending}>
              {insert.isPending ? "Inserting…" : "Insert"}
            </Button>
          </footer>
        </motion.div>
      </motion.div>
    </AnimatePresence>
  );
}
