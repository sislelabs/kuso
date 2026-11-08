"use client";

import { forwardRef, useImperativeHandle, useRef } from "react";
import { AlertTriangle } from "lucide-react";
import { cn } from "@/lib/utils";
import type { RawIssue } from "./rawEnv";

export interface RawEnvEditorHandle {
  goToLine: (line: number) => void;
}

// RawEnvEditor is the whole-set .env textarea: a line-number gutter that
// marks lines with parse errors, and the error list underneath with
// click-to-jump. Parsing happens in the parent (rawEnv.reconcileRaw).
export const RawEnvEditor = forwardRef<
  RawEnvEditorHandle,
  {
    value: string;
    onChange: (v: string) => void;
    errors: RawIssue[];
    readOnly?: boolean;
  }
>(function RawEnvEditor({ value, onChange, errors, readOnly }, ref) {
  const taRef = useRef<HTMLTextAreaElement>(null);
  const gutterRef = useRef<HTMLDivElement>(null);
  const lineCount = Math.max(1, value.split("\n").length);
  const errorLines = new Set(errors.map((e) => e.line));

  const goToLine = (line: number) => {
    const ta = taRef.current;
    if (!ta) return;
    const lines = value.split("\n");
    let start = 0;
    for (let i = 0; i < line - 1 && i < lines.length; i++) start += lines[i].length + 1;
    const end = start + (lines[line - 1]?.length ?? 0);
    ta.focus();
    ta.setSelectionRange(start, end);
    // Approximate line height scroll; the textarea uses a fixed leading.
    const lh = parseFloat(getComputedStyle(ta).lineHeight) || 20;
    ta.scrollTop = Math.max(0, (line - 3) * lh);
  };
  useImperativeHandle(ref, () => ({ goToLine }));

  return (
    <div className="space-y-2">
      <div
        className={cn(
          "flex overflow-hidden rounded-md border bg-[var(--bg-secondary)]",
          errors.length ? "border-[var(--error)]/60" : "border-[var(--border-subtle)] focus-within:border-[var(--border-strong)]",
        )}
      >
        <div
          ref={gutterRef}
          aria-hidden
          className="select-none overflow-hidden border-r border-[var(--border-subtle)] py-3 pl-2 pr-2 text-right font-mono text-xs leading-5 text-[var(--text-tertiary)]"
          style={{ height: "min(60vh, 32rem)" }}
        >
          {Array.from({ length: lineCount }, (_, i) => (
            <div key={i} className={cn(errorLines.has(i + 1) && "font-semibold text-[var(--error)]")}>
              {i + 1}
            </div>
          ))}
        </div>
        <textarea
          ref={taRef}
          value={value}
          readOnly={readOnly}
          onChange={(e) => onChange(e.target.value)}
          onScroll={(e) => {
            if (gutterRef.current) gutterRef.current.scrollTop = e.currentTarget.scrollTop;
          }}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          wrap="off"
          aria-label="Variables as .env"
          aria-invalid={errors.length > 0}
          placeholder={"DATABASE_URL=${{ db.DATABASE_URL }}\nNODE_ENV=production"}
          className="min-w-0 flex-1 resize-none overflow-auto bg-transparent px-3 py-3 font-mono text-xs leading-5 text-[var(--text-primary)] outline-none placeholder:text-[var(--text-tertiary)]"
          style={{ height: "min(60vh, 32rem)", tabSize: 2 }}
        />
      </div>
      {errors.length > 0 ? (
        <div role="alert" className="rounded-md border border-[var(--error)]/40 bg-[var(--error-subtle)] px-3 py-2">
          <p className="flex items-center gap-1.5 text-xs font-medium text-[var(--error)]">
            <AlertTriangle className="h-3.5 w-3.5" />
            {errors.length === 1 ? "1 line needs fixing" : `${errors.length} lines need fixing`} before saving
          </p>
          <ul className="mt-1 space-y-0.5">
            {errors.slice(0, 8).map((e, i) => (
              <li key={i} className="text-xs text-[var(--text-primary)]">
                <button
                  type="button"
                  onClick={() => goToLine(e.line)}
                  className="mr-1.5 font-mono text-[var(--error)] underline-offset-2 hover:underline"
                >
                  Line {e.line}
                </button>
                {e.message}
              </li>
            ))}
            {errors.length > 8 && (
              <li className="text-xs text-[var(--text-secondary)]">and {errors.length - 8} more</li>
            )}
          </ul>
        </div>
      ) : (
        <p className="text-xs text-[var(--text-tertiary)]">
          One <span className="font-mono">KEY=value</span> per line. <span className="font-mono">#</span> comments,{" "}
          <span className="font-mono">export</span> and quotes work; double quotes can span lines. Removing a line
          deletes that variable.
        </p>
      )}
    </div>
  );
});
