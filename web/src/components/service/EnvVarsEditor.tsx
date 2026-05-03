"use client";

import { useState, useEffect, useMemo } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Trash2, Plus, Save, Eye, EyeOff, FileText, List, Link2 } from "lucide-react";
import { useServiceEnv, useSetServiceEnv } from "@/features/services";
import { listAddonSecretKeys } from "@/features/services/api";
import { useProject, useAddons } from "@/features/projects";
import { useQuery } from "@tanstack/react-query";
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

function toRow(v: KusoEnvVar, project: string): Row {
  const fromSecret = !!v.valueFrom;
  const raw = fromSecret ? "" : (v.value ?? "");
  return {
    name: v.name ?? "",
    // Reverse server-resolved literals back to ${{ x.KEY }} form so
    // the editor shows the original ref the user wrote. Without this
    // round-trip, every reload turns "${{api.URL}}" into the
    // expanded "http://e2e-test-api.kuso.svc.cluster.local:8080",
    // which is correct on the wire but ugly in the UI and wrong-ish
    // on save (the server would re-resolve and we'd lose the
    // refactor-friendly form).
    value: fromSecret ? "" : literalToRef(raw, project),
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

// literalToRef reverses the server-side service-ref resolution. When
// a value matches the cluster-local DNS shape we recognise, render it
// as the equivalent `${{ <svc>.<KEY> }}` token. The server will
// re-expand on save, so the round-trip is lossless.
//
// Patterns we recognise (URL first to avoid mis-classifying
// "http://host:port" as a HOST):
//   http://<svc-fqn>.<ns>.svc.cluster.local:<port> → ${{ svc.URL }}
//   <svc-fqn>.<ns>.svc.cluster.local              → ${{ svc.HOST }}
//
// project is needed to strip the "<project>-" prefix from the FQN
// back to the short name the user typed. Without it we'd guess at
// the dash position and break for projects with dashes (e.g.
// "e2e-test").
function literalToRef(value: string, project: string): string {
  if (!value) return value;
  const urlMatch = value.match(
    /^http:\/\/([a-z0-9-]+)\.[a-z0-9-]+\.svc\.cluster\.local(?::\d+)?$/
  );
  if (urlMatch) {
    return `\${{ ${stripProjectPrefix(urlMatch[1], project)}.URL }}`;
  }
  const hostMatch = value.match(
    /^([a-z0-9-]+)\.[a-z0-9-]+\.svc\.cluster\.local$/
  );
  if (hostMatch) {
    return `\${{ ${stripProjectPrefix(hostMatch[1], project)}.HOST }}`;
  }
  return value;
}

// stripProjectPrefix returns the user-friendly short name from a
// project-prefixed kube name. KusoService CRs are named
// "<project>-<short>"; the editor + canvas display the short form.
function stripProjectPrefix(fqn: string, project: string): string {
  const prefix = project + "-";
  if (fqn.startsWith(prefix)) return fqn.slice(prefix.length);
  return fqn;
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
      setRows((env.data.envVars ?? []).map((v) => toRow(v, project)));
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
              className="grid grid-cols-[180px_1fr_auto_auto_auto] items-center gap-1.5"
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
                placeholder={r.fromSecret ? "(from secret)" : "value or ${{ ref }}"}
                type={r.visible || r.fromSecret ? "text" : "password"}
                value={r.value}
                onChange={(e) => update(i, { value: e.target.value })}
                className="h-8 font-mono text-[12px]"
                disabled={r.fromSecret}
                spellCheck={false}
              />
              <ReferencePicker
                project={project}
                excludeService={service}
                onPick={(ref) => update(i, { value: ref, visible: true })}
                disabled={r.fromSecret}
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

      {mode === "rows" && (
        <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
          Use <span className="text-[var(--text-secondary)]">${"${{ <name>.<KEY> }}"}</span> to
          reference another service or addon. The icon to the right of any value picks
          the right ref for you.
        </p>
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

// ReferencePicker — dropdown that lets the user insert a `${{ x.KEY }}`
// reference into an env-var value. Shows services in the project
// (with HOST/PORT/URL/INTERNAL_URL synthetic keys) plus addons (with
// the keys actually present on each conn-secret). Service refs
// resolve to literal in-cluster DNS strings on save; addon refs
// resolve to secretKeyRef entries — both happen server-side, the
// picker just inserts the right ${{}} text.
function ReferencePicker({
  project,
  excludeService,
  onPick,
  disabled,
}: {
  project: string;
  excludeService: string;
  onPick: (ref: string) => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="relative">
      <button
        type="button"
        aria-label="Insert reference"
        title="Insert a reference to another service or addon"
        onClick={() => setOpen((v) => !v)}
        disabled={disabled}
        className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--accent)] disabled:opacity-30"
      >
        <Link2 className="h-3.5 w-3.5" />
      </button>
      {open && (
        <ReferenceMenu
          project={project}
          excludeService={excludeService}
          onPick={(ref) => {
            onPick(ref);
            setOpen(false);
          }}
          onClose={() => setOpen(false)}
        />
      )}
    </div>
  );
}

// ReferenceMenu is the dropdown contents — kept separate so the
// React Query hooks fire only when the menu is actually opened.
function ReferenceMenu({
  project,
  excludeService,
  onPick,
  onClose,
}: {
  project: string;
  excludeService: string;
  onPick: (ref: string) => void;
  onClose: () => void;
}) {
  const proj = useProject(project);
  const addons = useAddons(project);
  // Service entries with stripped project prefix so the user sees the
  // short name in the menu — same shape they typed when running
  // `kuso project service add`.
  const services = useMemo(() => {
    const list = (proj.data as { services?: { metadata: { name: string } }[] } | undefined)?.services ?? [];
    const prefix = project + "-";
    return list
      .map((s) => {
        const fqn = s.metadata.name;
        const short = fqn.startsWith(prefix) ? fqn.slice(prefix.length) : fqn;
        return short;
      })
      .filter((s) => s !== excludeService);
  }, [proj.data, project, excludeService]);

  // Auto-close on outside click + Escape.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <>
      <div className="fixed inset-0 z-40" onClick={onClose} aria-hidden />
      <div className="absolute right-0 top-9 z-50 w-72 max-h-[60vh] overflow-y-auto rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-1.5 shadow-[var(--shadow-lg)]">
        <p className="px-2 py-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          Services
        </p>
        {services.length === 0 ? (
          <p className="px-2 py-1.5 text-[11px] text-[var(--text-tertiary)]">
            No other services in this project.
          </p>
        ) : (
          services.map((svc) => <ServiceRefRow key={svc} service={svc} onPick={onPick} />)
        )}

        <div className="my-1.5 border-t border-[var(--border-subtle)]" />
        <p className="px-2 py-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          Addons
        </p>
        {(addons.data ?? []).length === 0 ? (
          <p className="px-2 py-1.5 text-[11px] text-[var(--text-tertiary)]">No addons.</p>
        ) : (
          (addons.data ?? []).map((a) => {
            const fqn = a.metadata.name;
            const prefix = project + "-";
            const short = fqn.startsWith(prefix) ? fqn.slice(prefix.length) : fqn;
            return (
              <AddonRefRow
                key={fqn}
                project={project}
                addonShort={short}
                onPick={onPick}
              />
            );
          })
        )}
      </div>
    </>
  );
}

// ServiceRefRow surfaces the four synthetic keys for a service. The
// list is fixed (HOST / PORT / URL / INTERNAL_URL) — no fetch needed.
function ServiceRefRow({ service, onPick }: { service: string; onPick: (ref: string) => void }) {
  const KEYS = ["URL", "INTERNAL_URL", "HOST", "PORT"];
  return (
    <div className="px-2 py-1">
      <p className="font-mono text-[11px] text-[var(--text-secondary)]">{service}</p>
      <div className="mt-1 flex flex-wrap gap-1">
        {KEYS.map((k) => (
          <button
            key={k}
            type="button"
            onClick={() => onPick(`\${{ ${service}.${k} }}`)}
            className="rounded border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-secondary)] hover:border-[var(--accent)]/40 hover:text-[var(--accent)]"
            title={`Insert \${{ ${service}.${k} }}`}
          >
            {k}
          </button>
        ))}
      </div>
    </div>
  );
}

// AddonRefRow fetches the addon's connection-secret keys and renders
// each as a clickable chip. Lazy fetched (only when the menu opens)
// so the editor doesn't pay the round-trips up front.
function AddonRefRow({
  project,
  addonShort,
  onPick,
}: {
  project: string;
  addonShort: string;
  onPick: (ref: string) => void;
}) {
  const keys = useQuery({
    queryKey: ["addons", project, addonShort, "secret-keys"],
    queryFn: () => listAddonSecretKeys(project, addonShort),
    staleTime: 60_000,
  });
  return (
    <div className="px-2 py-1">
      <p className="font-mono text-[11px] text-[var(--text-secondary)]">{addonShort}</p>
      <div className="mt-1 flex flex-wrap gap-1">
        {keys.isPending && (
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">loading…</span>
        )}
        {keys.isError && (
          <span className="font-mono text-[10px] text-amber-400">no keys yet</span>
        )}
        {(keys.data?.keys ?? []).map((k) => (
          <button
            key={k}
            type="button"
            onClick={() => onPick(`\${{ ${addonShort}.${k} }}`)}
            className="rounded border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-secondary)] hover:border-[var(--accent)]/40 hover:text-[var(--accent)]"
          >
            {k}
          </button>
        ))}
      </div>
    </div>
  );
}
