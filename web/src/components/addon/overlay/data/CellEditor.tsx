"use client";

import { useState } from "react";
import type { SQLColumn, SQLCellValue } from "@/features/projects";
import { inputKindFor, validateCell, toCellValue } from "./cellInput";

// CellEditor is a single grid cell that switches to a type-aware input on
// double-click. On commit it validates, then calls onSave with the { value,
// isNull } wire shape. NULL is a distinct, explicit state (dim "null" token,
// toggled with the ∅ button) — never conflated with an empty string.
export function CellEditor({
  col,
  value,
  isNull,
  onSave,
}: {
  col: SQLColumn;
  value: string;
  isNull: boolean;
  onSave: (v: SQLCellValue) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(value);
  const [draftNull, setDraftNull] = useState(isNull);
  const [err, setErr] = useState<string | null>(null);
  const kind = inputKindFor(col);

  const begin = () => {
    setDraft(value);
    setDraftNull(isNull);
    setErr(null);
    setEditing(true);
  };

  const commit = () => {
    const e = validateCell(col, draft, draftNull);
    if (e) {
      setErr(e);
      return;
    }
    setEditing(false);
    // No-op if unchanged.
    if (draftNull === isNull && draft === value) return;
    onSave(toCellValue(col, draft, draftNull));
  };

  const cancel = () => {
    setEditing(false);
    setErr(null);
  };

  if (!editing) {
    return (
      <div
        className="cursor-text px-2 py-1 text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]/40"
        onDoubleClick={begin}
        title="double-click to edit"
      >
        {isNull ? (
          <span className="italic text-[var(--text-tertiary)]/60">null</span>
        ) : value === "" ? (
          <span className="text-[var(--text-tertiary)]/40">&nbsp;</span>
        ) : value.length > 200 ? (
          value.slice(0, 200) + "…"
        ) : (
          value
        )}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-0.5 bg-[var(--bg-primary)] p-1">
      <div className="flex items-center gap-1">
        {col.nullable && (
          <button
            type="button"
            onClick={() => setDraftNull((n) => !n)}
            className={`shrink-0 rounded px-1 text-[9px] ${
              draftNull
                ? "bg-amber-500/20 text-amber-300"
                : "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)]"
            }`}
            title="toggle NULL"
          >
            ∅
          </button>
        )}
        {draftNull ? (
          <span className="flex-1 px-1 text-[11px] italic text-[var(--text-tertiary)]/60">null</span>
        ) : kind === "bool" ? (
          <select
            autoFocus
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            className="flex-1 rounded border border-[var(--border-strong)] bg-[var(--bg-primary)] px-1 py-0.5 text-[11px]"
          >
            <option value="true">true</option>
            <option value="false">false</option>
          </select>
        ) : kind === "enum" ? (
          <select
            autoFocus
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            className="flex-1 rounded border border-[var(--border-strong)] bg-[var(--bg-primary)] px-1 py-0.5 text-[11px]"
          >
            {(col.enumValues ?? []).map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        ) : kind === "json" ? (
          <textarea
            autoFocus
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            rows={3}
            spellCheck={false}
            onKeyDown={(e) => {
              if (e.key === "Escape") cancel();
            }}
            className="flex-1 resize-y rounded border border-[var(--border-strong)] bg-[var(--bg-primary)] px-1 py-0.5 font-mono text-[11px]"
          />
        ) : (
          <input
            autoFocus
            type={kind === "number" ? "text" : "text"}
            inputMode={kind === "number" ? "decimal" : undefined}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") commit();
              if (e.key === "Escape") cancel();
            }}
            className="flex-1 rounded border border-[var(--border-strong)] bg-[var(--bg-primary)] px-1 py-0.5 font-mono text-[11px]"
          />
        )}
      </div>
      {err && <span className="px-1 text-[9px] text-red-400">{err}</span>}
      <div className="flex justify-end gap-1">
        <button
          type="button"
          onClick={cancel}
          className="rounded px-1.5 py-0.5 text-[9px] text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)]"
        >
          esc
        </button>
        <button
          type="button"
          onClick={commit}
          className="rounded bg-[var(--accent)]/20 px-1.5 py-0.5 text-[9px] text-[var(--accent)] hover:bg-[var(--accent)]/30"
        >
          save
        </button>
      </div>
    </div>
  );
}
