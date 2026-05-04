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

// addonByConnSecret maps "<project>-<addon>-conn" → "<addon>" so the
// editor can detect a secretKeyRef that originally came from an addon
// ref like ${{ postgres.DATABASE_URL }} and render it as a ref again
// instead of an opaque (from secret) row. Without this the round-trip
// — type ref, save, reload — collapses to a disabled placeholder and
// the user can't edit their own value.
function addonShortByConnSecret(
  addons: ReadonlyArray<{ metadata: { name: string }; status?: { connectionSecret?: string } }>,
  project: string
): Map<string, string> {
  const out = new Map<string, string>();
  const prefix = project + "-";
  for (const a of addons) {
    const fqn = a.metadata?.name ?? "";
    const short = fqn.startsWith(prefix) ? fqn.slice(prefix.length) : fqn;
    const sec = a.status?.connectionSecret;
    if (sec) out.set(sec, short);
    // Fallback: addons without a populated status yet still follow the
    // canonical "<fqn>-conn" naming. Index that too so freshly-created
    // addons round-trip before the operator backfills status.
    if (fqn) out.set(fqn + "-conn", short);
  }
  return out;
}

function toRow(
  v: KusoEnvVar,
  project: string,
  addonByConn: Map<string, string>
): Row {
  // Detect "secretKeyRef pointing at a known addon" and render it as a
  // ${{ <addon>.<KEY> }} ref instead of treating it as opaque. Anything
  // else with a valueFrom (manual secretKeyRef, fieldRef, etc.) stays
  // fromSecret because we have no user-facing representation for it.
  const ref = addonRefFromValueFrom(v.valueFrom, addonByConn);
  if (ref) {
    return { name: v.name ?? "", value: ref, fromSecret: false, visible: false };
  }
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

// addonRefFromValueFrom picks the secretKeyRef out of a valueFrom blob
// (server returns it as `Record<string, unknown>` to stay forward-compat
// with future kube valueFrom variants) and, if it points at a known
// addon's connection secret, returns the equivalent `${{ <addon>.<KEY> }}`
// ref. Returns "" when the secretKeyRef is opaque (manually-mounted
// secret unrelated to a kuso addon) so the caller falls back to the
// fromSecret display path.
function addonRefFromValueFrom(
  vf: Record<string, unknown> | undefined,
  addonByConn: Map<string, string>
): string {
  if (!vf) return "";
  const skr = vf.secretKeyRef as { name?: string; key?: string } | undefined;
  if (!skr || !skr.name || !skr.key) return "";
  const short = addonByConn.get(skr.name);
  if (!short) return "";
  return `\${{ ${short}.${skr.key} }}`;
}

function toEnvVar(r: Row): KusoEnvVar {
  if (r.fromSecret) {
    return { name: r.name.trim() };
  }
  return { name: r.name.trim(), value: r.value };
}

// Valid POSIX-ish env-var name: starts with a letter or underscore,
// rest letter/digit/underscore. Required because k8s accepts the
// CR but the kubelet drops invalid names from the pod env silently
// — the user types "FOO BAR" and gets nothing on the pod.
const ENV_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

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
  const addons = useAddons(project);
  // Memoised so the toRow effect below only re-runs when the addon set
  // (or its connectionSecret status fields) actually changes. Without
  // memo, every re-render rebuilds the map and the effect's dep array
  // would point at a fresh reference each time.
  const addonByConn = useMemo(
    () => addonShortByConnSecret(addons.data ?? [], project),
    [addons.data, project]
  );
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
      setRows((env.data.envVars ?? []).map((v) => toRow(v, project, addonByConn)));
      setDirty(false);
    }
  }, [env.data, addonByConn, project]);

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
    // Trim + validate before submit. Three rules:
    //   1. Drop rows whose name is empty after trim.
    //   2. Drop rows that have neither a value nor a fromSecret
    //      backing — they'd round-trip as ghost entries that the
    //      pod ignores but the editor keeps re-displaying.
    //   3. Reject invalid env-var names (POSIX rule) and duplicate
    //      names with explicit toasts so the user knows what got
    //      caught.
    const seen = new Set<string>();
    const cleaned: KusoEnvVar[] = [];
    for (const r of rows) {
      const name = r.name.trim();
      if (!name) continue;
      if (!ENV_NAME_RE.test(name)) {
        toast.error(`Invalid env var name "${name}" — letters, digits, underscore only`);
        return;
      }
      if (seen.has(name)) {
        toast.error(`Duplicate env var name "${name}"`);
        return;
      }
      seen.add(name);
      // Drop empty literal rows. Secret-backed rows are kept since
      // they have a valueFrom regardless of value.
      if (!r.fromSecret && r.value === "") continue;
      cleaned.push(toEnvVar(r));
    }
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

      {/* Inherited env vars — keys that flow in from project-level
          and instance-level shared secrets. Read-only display with a
          link to the place to edit. Helps users understand WHY their
          service has DATABASE_URL or SENTRY_DSN without having defined
          it locally. Show even when empty so the affordance is
          discoverable on day 1. */}
      <InheritedSection project={project} />

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
          Use <span className="text-[var(--text-secondary)]">{"${{ <name>.<KEY> }}"}</span> to
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
// (with HOST/PORT/URL/INTERNAL_URL plus PUBLIC_HOST/PUBLIC_URL
// synthetic keys) plus addons (with the keys actually present on
// each conn-secret). Service refs resolve to literal strings on save
// — in-cluster DNS for URL/INTERNAL_URL, the public domain for
// PUBLIC_URL — and addon refs resolve to secretKeyRef entries.
// All resolution happens server-side; the picker just inserts the
// right ${{}} text.
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

// ServiceRefRow surfaces the synthetic keys for a service. PUBLIC_URL
// resolves to the externally-reachable URL (custom domain or auto
// kuso domain) so a frontend pointing at an API picks the right
// surface; URL/INTERNAL_URL stays in-cluster for backend↔backend.
function ServiceRefRow({ service, onPick }: { service: string; onPick: (ref: string) => void }) {
  const KEYS = ["URL", "INTERNAL_URL", "HOST", "PORT", "PUBLIC_URL", "PUBLIC_HOST"];
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

// InheritedSection renders the read-only "inherited from" panel
// at the top of the env editor. Two stacked groups:
//
//   - From <project>-shared (links to /projects/<p>/settings)
//   - From kuso-instance-shared (links to /settings/instance-secrets)
//
// Each shows the keys; values are write-only on the server and we
// don't even ask for them — just the existence is the signal we
// surface. Clicking the "edit →" link takes the user to the proper
// settings page. Empty groups still render the affordance in muted
// text so the discoverability story is "open the env editor, see
// what's inherited" without needing to read docs.
function InheritedSection({ project }: { project: string }) {
  const projectKeys = useQuery<{ keys: string[] }>({
    queryKey: ["projects", project, "shared-secrets"],
    queryFn: () =>
      fetch(`/api/projects/${encodeURIComponent(project)}/shared-secrets`, {
        headers: authHeaders(),
      }).then((r) => (r.ok ? r.json() : { keys: [] })),
    staleTime: 60_000,
  });
  const instanceKeys = useQuery<{ keys: string[] }>({
    queryKey: ["instance-secrets"],
    queryFn: () =>
      fetch(`/api/instance-secrets`, { headers: authHeaders() }).then((r) =>
        // Non-admins get 403; treat as empty rather than error.
        r.ok ? r.json() : { keys: [] }
      ),
    staleTime: 60_000,
    retry: false,
    throwOnError: false,
  });
  const pk = projectKeys.data?.keys ?? [];
  const ik = instanceKeys.data?.keys ?? [];
  if (pk.length === 0 && ik.length === 0) {
    return (
      <details className="group rounded-md border border-dashed border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-1.5">
        <summary className="cursor-pointer list-none font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]">
          inherited env vars · 0 from project · 0 from instance
        </summary>
        <p className="mt-2 font-mono text-[10px] text-[var(--text-tertiary)]">
          Project-level vars are configured in{" "}
          <a
            href={`/projects/${encodeURIComponent(project)}/settings`}
            className="text-[var(--accent)] hover:underline"
          >
            project settings
          </a>
          . Instance-level vars are admin-only at{" "}
          <a href="/settings/instance-secrets" className="text-[var(--accent)] hover:underline">
            /settings/instance-secrets
          </a>
          .
        </p>
      </details>
    );
  }
  return (
    <details
      open
      className="group rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40"
    >
      <summary className="flex cursor-pointer list-none items-center justify-between px-3 py-1.5 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        <span>
          inherited env vars · {pk.length} from project · {ik.length} from instance
        </span>
        <span className="text-[var(--text-tertiary)] group-open:rotate-90 transition-transform">
          ›
        </span>
      </summary>
      <div className="space-y-2 border-t border-[var(--border-subtle)] px-3 py-2">
        <InheritedGroup
          label={`from ${project}-shared`}
          editHref={`/projects/${encodeURIComponent(project)}/settings`}
          keys={pk}
        />
        <InheritedGroup
          label="from kuso-instance-shared"
          editHref="/settings/instance-secrets"
          keys={ik}
        />
      </div>
    </details>
  );
}

function InheritedGroup({
  label,
  editHref,
  keys,
}: {
  label: string;
  editHref: string;
  keys: string[];
}) {
  return (
    <div>
      <div className="flex items-center justify-between">
        <p className="font-mono text-[10px] text-[var(--text-tertiary)]">{label}</p>
        <a
          href={editHref}
          className="font-mono text-[10px] text-[var(--accent)] hover:underline"
        >
          edit →
        </a>
      </div>
      {keys.length === 0 ? (
        <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]/60">
          (none)
        </p>
      ) : (
        <div className="mt-1 flex flex-wrap gap-1">
          {keys.sort().map((k) => (
            <span
              key={k}
              className="inline-flex items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-0.5 font-mono text-[10px]"
              title="read-only — edit at the source"
            >
              <span className="text-[var(--text-tertiary)]">🔒</span>
              {k}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

// authHeaders reads the JWT cookie/localStorage the same way the
// api() wrapper does. Inline because this component reaches outside
// the standard `api()` helper to gate-fail silently on 403 (when
// the user isn't admin and asks for instance secrets) instead of
// triggering the global 401 redirect.
function authHeaders(): Record<string, string> {
  if (typeof window === "undefined") return {};
  const m = document.cookie.match(/(?:^|; )kuso\.JWT_TOKEN=([^;]+)/);
  const tok = m ? decodeURIComponent(m[1]) : window.localStorage.getItem("kuso.jwt");
  return tok ? { Authorization: `Bearer ${tok}` } : {};
}
