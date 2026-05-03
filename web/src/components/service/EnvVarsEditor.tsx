"use client";

import { useState, useEffect, useMemo } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Trash2, Plus, Save, Eye, EyeOff, FileText, List } from "lucide-react";
import { useServiceEnv, useSetServiceEnv } from "@/features/services";
import { useCan, Perms } from "@/features/auth";
import type { KusoEnvVar } from "@/types/projects";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

interface Row {
  name: string;
  value: string;
  fromSecret: boolean;
  visible: boolean;
}

type Mode = "rows" | "bulk";

function toRow(v: KusoEnvVar): Row {
  const fromSecret = !!v.valueFrom;
  return {
    name: v.name ?? "",
    value: fromSecret ? "" : (v.value ?? ""),
    fromSecret,
    visible: false,
  };
}

function toEnvVar(r: Row): KusoEnvVar {
  if (r.fromSecret) {
    return { name: r.name };
  }
  return { name: r.name, value: r.value };
}

// Serialize plain (non-secret) rows to dotenv format. Secret-backed
// rows are emitted as a comment so the user sees them in the bulk
// view but can't accidentally rewrite them as plain values.
function rowsToDotenv(rows: Row[]): string {
  return rows
    .map((r) => {
      if (r.fromSecret) return `# ${r.name}=<from secret>`;
      const v = r.value ?? "";
      // Quote when the value contains whitespace, =, or # so the
      // round-trip parse picks it back up unchanged.
      const needsQuotes = /[\s#"=]/.test(v) || v === "";
      const escaped = v.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
      return needsQuotes ? `${r.name}="${escaped}"` : `${r.name}=${v}`;
    })
    .join("\n");
}

// Parse dotenv-ish text. Each non-empty, non-comment line is split on
// the first '=' into key/value. Surrounding double quotes are
// stripped and \" / \\ are unescaped. Anything that doesn't match a
// valid `KEY=value` pattern is silently dropped — the textarea is
// the user's pasteboard, not a strict parser.
function dotenvToRows(text: string, prevSecrets: Row[]): Row[] {
  const out: Row[] = [];
  const lines = text.split(/\r?\n/);
  for (const raw of lines) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    const name = line.slice(0, eq).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) continue;
    let value = line.slice(eq + 1).trim();
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value
        .slice(1, -1)
        .replace(/\\"/g, '"')
        .replace(/\\\\/g, "\\");
    }
    out.push({ name, value, fromSecret: false, visible: false });
  }
  // Preserve any secret-backed entries — they aren't representable in
  // the bulk textarea, so we re-attach them after parsing so the user
  // doesn't accidentally lose them.
  for (const s of prevSecrets) {
    if (!out.some((r) => r.name === s.name)) out.push(s);
  }
  return out;
}

export function EnvVarsEditor({ project, service }: { project: string; service: string }) {
  const env = useServiceEnv(project, service);
  const setEnv = useSetServiceEnv(project, service);
  // secrets:write gates the Save + the per-row destructive
  // affordances. We intentionally KEEP the values visible (env vars
  // are already fetched here; if the user can't see them they
  // shouldn't be in this tab) — this is purely about mutation.
  const canWrite = useCan(Perms.SecretsWrite);
  const [rows, setRows] = useState<Row[]>([]);
  const [dirty, setDirty] = useState(false);
  const [mode, setMode] = useState<Mode>("rows");
  const [bulkText, setBulkText] = useState("");

  useEffect(() => {
    if (env.data) {
      setRows((env.data.envVars ?? []).map(toRow));
      setDirty(false);
    }
  }, [env.data]);

  // Bulk text is derived from rows when entering bulk mode and
  // committed back to rows on every keystroke. We keep them in sync
  // so the user can flip between modes mid-edit without losing work.
  const enterBulk = () => {
    setBulkText(rowsToDotenv(rows));
    setMode("bulk");
  };
  const exitBulk = () => {
    setMode("rows");
  };
  const onBulkChange = (text: string) => {
    setBulkText(text);
    const secrets = rows.filter((r) => r.fromSecret);
    setRows(dotenvToRows(text, secrets));
    setDirty(true);
  };

  const update = (idx: number, patch: Partial<Row>) => {
    setRows((prev) => prev.map((r, i) => (i === idx ? { ...r, ...patch } : r)));
    setDirty(true);
  };
  const remove = (idx: number) => {
    setRows((prev) => prev.filter((_, i) => i !== idx));
    setDirty(true);
  };
  const add = () => {
    setRows((prev) => [...prev, { name: "", value: "", fromSecret: false, visible: true }]);
    setDirty(true);
  };

  const save = async () => {
    const cleaned = rows.filter((r) => r.name.trim().length > 0).map(toEnvVar);
    try {
      await setEnv.mutateAsync(cleaned);
      toast.success("Env vars saved");
      setDirty(false);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save env vars");
    }
  };

  const visibleCount = useMemo(
    () => rows.filter((r) => !r.fromSecret || r.name).length,
    [rows]
  );

  if (env.isPending) {
    return <div className="text-sm text-[var(--text-tertiary)]">loading…</div>;
  }
  if (env.isError) {
    return (
      <div className="text-sm text-red-500">
        Failed to load env vars: {env.error?.message}
      </div>
    );
  }

  return (
    <div className="space-y-3">
      {/* Mode toggle — segmented control flips between per-row chips
          and a dotenv textarea. Both write to the same `rows` state
          so flipping is lossless. */}
      <div className="flex items-center justify-between">
        <div className="inline-flex rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-0.5 text-[11px]">
          <button
            type="button"
            onClick={() => mode === "bulk" && exitBulk()}
            className={cn(
              "inline-flex items-center gap-1.5 rounded px-2 py-1 transition-colors",
              mode === "rows"
                ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
            )}
          >
            <List className="h-3 w-3" />
            Rows
          </button>
          <button
            type="button"
            onClick={() => mode === "rows" && enterBulk()}
            className={cn(
              "inline-flex items-center gap-1.5 rounded px-2 py-1 transition-colors",
              mode === "bulk"
                ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
            )}
          >
            <FileText className="h-3 w-3" />
            Bulk
          </button>
        </div>
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
          {visibleCount} {visibleCount === 1 ? "var" : "vars"}
        </span>
      </div>

      {mode === "rows" ? (
        <div className="space-y-1.5">
          {rows.length === 0 && (
            <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-6 text-center text-xs text-[var(--text-tertiary)]">
              No env vars. Click <span className="font-mono">Add</span> or paste a{" "}
              <span className="font-mono">.env</span> file via Bulk mode.
            </p>
          )}
          {rows.map((r, i) => (
            <div
              key={i}
              className="grid grid-cols-[180px_1fr_auto_auto] items-center gap-1.5"
            >
              <Input
                placeholder="KEY"
                value={r.name}
                onChange={(e) => update(i, { name: e.target.value })}
                className="h-8 font-mono text-[12px]"
                disabled={r.fromSecret}
                spellCheck={false}
              />
              <Input
                placeholder={r.fromSecret ? "(from secret)" : "value"}
                type={r.visible || r.fromSecret ? "text" : "password"}
                value={r.value}
                onChange={(e) => update(i, { value: e.target.value })}
                className="h-8 font-mono text-[12px]"
                disabled={r.fromSecret}
                spellCheck={false}
              />
              <button
                type="button"
                aria-label={r.visible ? "Hide" : "Show"}
                onClick={() => update(i, { visible: !r.visible })}
                disabled={r.fromSecret}
                className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-30"
              >
                {r.visible ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
              </button>
              <button
                type="button"
                aria-label="Remove"
                onClick={() => remove(i)}
                disabled={r.fromSecret}
                className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400 disabled:opacity-30"
              >
                <Trash2 className="h-3.5 w-3.5" />
              </button>
            </div>
          ))}
        </div>
      ) : (
        <div className="space-y-1.5">
          <textarea
            value={bulkText}
            onChange={(e) => onBulkChange(e.target.value)}
            spellCheck={false}
            placeholder={"DATABASE_URL=postgres://...\nREDIS_URL=redis://...\nNODE_ENV=production"}
            rows={Math.max(8, Math.min(20, bulkText.split("\n").length + 1))}
            className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3 font-mono text-[12px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
          />
          <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
            One <span className="text-[var(--text-secondary)]">KEY=value</span> per line. Quote
            values with whitespace. Secret-backed entries appear as comments and stay attached.
          </p>
        </div>
      )}

      <div className="flex items-center gap-2">
        {mode === "rows" && (
          <Button variant="outline" size="sm" onClick={add} type="button">
            <Plus className="h-3.5 w-3.5" /> Add
          </Button>
        )}
        {canWrite ? (
          <Button
            size="sm"
            onClick={save}
            type="button"
            disabled={!dirty || setEnv.isPending}
          >
            <Save className="h-3.5 w-3.5" />
            {setEnv.isPending ? "Saving…" : "Save"}
          </Button>
        ) : (
          <span
            className="font-mono text-[10px] text-[var(--text-tertiary)]"
            title="secrets:write permission required"
          >
            read-only
          </span>
        )}
        {dirty && canWrite && (
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            unsaved changes
          </span>
        )}
      </div>
    </div>
  );
}
