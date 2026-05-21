"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useOverlayDirty } from "@/components/service/ServiceOverlay";
import { Button } from "@/components/ui/button";
import { DiffConfirmDialog, type DiffEntry } from "@/components/shared/DiffConfirmDialog";
import { Input } from "@/components/ui/input";
import { Trash2, Plus, Eye, EyeOff, FileText, List, Link2, AlertCircle, Wand2 } from "lucide-react";
import { useServiceEnv, useSetServiceEnv, useDetectedEnv, useDrift } from "@/features/services";
import type { DetectedEnv } from "@/features/services/api";
import { listAddonSecretKeys } from "@/features/services/api";
import { useProject, useAddons } from "@/features/projects";
import { useQuery } from "@tanstack/react-query";
import { useCan, Perms } from "@/features/auth";
import { api, ApiError } from "@/lib/api-client";
import type { KusoEnvVar } from "@/types/projects";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

interface Row {
  // Stable per-row id assigned at row-creation time. React's `key`
  // attribute reads this so renaming the var (which mutates `name`
  // on every keystroke) doesn't unmount the row + steal focus from
  // the input. Generated client-side; never persisted.
  id: string;
  name: string;
  value: string;
  fromSecret: boolean;
  visible: boolean;
}

// rid mints a fresh id for a Row. Math.random is fine — these
// only need to be unique within the current editor session.
function rid(): string {
  return Math.random().toString(36).slice(2, 10);
}

// reservedEnvWarning mirrors server-go's projects.envNameReserved
// rules so the user sees the conflict at typing time, not at save
// time. Server is still authoritative — these are nudges. Returns
// the reason string when the name is reserved; empty string otherwise.
function reservedEnvWarning(name: string): string {
  if (!name) return "";
  if (name === "PORT") {
    return "PORT is set by kuso from Settings → Networking → Port";
  }
  if (name === "HOSTNAME") {
    return "HOSTNAME is reserved by the kubelet";
  }
  if (name.startsWith("KUBERNETES_")) {
    return "KUBERNETES_* is reserved for in-cluster API access";
  }
  return "";
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
    return { id: rid(), name: v.name ?? "", value: ref, fromSecret: false, visible: false };
  }
  const fromSecret = !!v.valueFrom;
  const raw = fromSecret ? "" : (v.value ?? "");
  return {
    id: rid(),
    name: v.name ?? "",
    // Reverse server-resolved literals back to ${{ x.KEY }} form so
    // the editor shows the original ref the user wrote.
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

// formatEnvForDiff renders a KusoEnvVar as a single line for the
// diff modal. Secret-backed entries are masked behind <secret-ref>
// rather than dumping the secret name into the diff text — the
// user only needs to see "VAR is now sourced from a secret", not
// which key on which secret. Literal values are clipped to 60 chars
// so a long DATABASE_URL doesn't push the modal off-screen.
function formatEnvForDiff(v: KusoEnvVar): string {
  if (v.valueFrom) return "<secret>";
  const val = v.value ?? "";
  if (val.length > 60) return val.slice(0, 57) + "…";
  return val;
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
    out.push({ id: rid(), name, value, fromSecret: false, visible: false });
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
  const detected = useDetectedEnv(project, service);
  const drift = useDrift(project, service);
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
  // Register dirty + save with the overlay shell so the unified
  // SaveBar fires onSave for this panel. The previous version only
  // registered dirty (for the ESC-prompt) but kept its own inline
  // Save button — so users on a 1280-wide screen saw two save
  // affordances (overlay SaveBar + inline button) for the same
  // edit. Funnelling save through the shell removes the duplicate
  // and matches ServiceSettingsPanel's pattern.
  //
  // The callbacks have to be set up via refs because save/reset
  // close over `rows` + `baselineFromRows`, both of which only exist
  // after this hook in the component body. The hook reads the ref at
  // SaveBar-click time, so the latest closure is the one that fires.
  const saveRef = useRef<() => void>(() => {});
  const discardRef = useRef<() => void>(() => {});
  // setEnv.error stays populated after a failed mutation until the
  // next mutate() resets it; surface it through the SaveBar so the
  // user sees a sticky reason for the failure (instead of a 4s toast
  // that disappears while they're still reading it).
  useOverlayDirty("variables", dirty && canWrite, {
    onSave: () => saveRef.current(),
    onDiscard: () => discardRef.current(),
    saving: setEnv.isPending,
    saveError: setEnv.error instanceof Error ? setEnv.error.message : undefined,
  });
  // Tracks the last server-known row set so the concurrent-edit
  // detector can compare incoming refetches against the baseline,
  // not the local (possibly-edited) rows.
  const baselineFromRows = useRef<Row[]>([]);
  const [mode, setMode] = useState<Mode>("rows");
  const [bulkText, setBulkText] = useState("");
  // Sticky "rolled out" window. Tied ONLY to the local savedAt set
  // in this session's save() — refresh wipes it deliberately.
  // Showing a banner from server-side lastRolloutAt would lie when
  // someone else's save (or a build promote, or any pod restart)
  // happened recently — the user opening the page fresh has no
  // context for "change is live", they didn't change anything.
  // Server-side drift.podsStale is the honest signal for that case.
  const [savedAt, setSavedAt] = useState<number | null>(null);
  // Re-render every 5s while the sticky banner is visible so the
  // "Ns ago" text ticks and the banner clears 60s after save without
  // requiring user interaction.
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (savedAt == null) return;
    const remaining = 60_000 - (Date.now() - savedAt);
    if (remaining <= 0) return;
    const t = setInterval(() => setNow(Date.now()), 5_000);
    const clear = setTimeout(() => setSavedAt(null), remaining);
    return () => {
      clearInterval(t);
      clearTimeout(clear);
    };
  }, [savedAt]);
  const stickySaved = savedAt != null && now - savedAt < 60_000;
  const ageSec = savedAt != null ? Math.max(0, Math.floor((now - savedAt) / 1000)) : 0;

  // Concurrent-edit guard: when env.data refetches, only re-baseline
  // the rows when the user has nothing dirty. Otherwise we'd silently
  // wipe in-progress edits the moment a teammate saved upstream.
  // Surface a one-shot toast so the user knows a remote change came
  // in — they can save (PATCH wins; server retries on conflict) or
  // reload to pick up the upstream version.
  const [conflictNotified, setConflictNotified] = useState(false);
  useEffect(() => {
    if (!env.data) return;
    // Alphabetical (case-insensitive). Server returns env vars in
    // insertion order which is meaningless to a human reading a
    // 30-var list. Sorting client-side keeps the storage order
    // intact (the server still sees whatever order PATCH posts —
    // which IS sorted as a side effect, but that's fine; env-var
    // order has no semantic meaning).
    const incoming = (env.data.envVars ?? [])
      .map((v) => toRow(v, project, addonByConn))
      .sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
    if (!dirty) {
      setRows(incoming);
      baselineFromRows.current = incoming;
      setConflictNotified(false);
      return;
    }
    // Dirty + remote change: keep local edits, warn once. The PATCH
    // path is last-write-wins on the server, so a save will still go
    // through, but the user should know they're on top of someone
    // else's change.
    if (!conflictNotified && !rowsShallowEqual(incoming, baselineFromRows.current)) {
      toast("Another edit landed on this service. Save will overwrite it; reload to merge.");
      setConflictNotified(true);
    }
    // Always update the baseline ref so a refetch-then-discard maps
    // back to the latest server state.
    baselineFromRows.current = incoming;
  }, [env.data, addonByConn, project, dirty, conflictNotified]);

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
    setRows((prev) => [...prev, { id: rid(), name: "", value: "", fromSecret: false, visible: true }]);
    setDirty(true);
  };

  // Two-step save. cleanRows() validates + dedups; the result is
  // either the proposed payload, or null when validation toast'd.
  const cleanRows = (): KusoEnvVar[] | null => {
    const seen = new Set<string>();
    const cleaned: KusoEnvVar[] = [];
    for (const r of rows) {
      const name = r.name.trim();
      if (!name) continue;
      if (!ENV_NAME_RE.test(name)) {
        toast.error(`Invalid env var name "${name}" — letters, digits, underscore only`);
        return null;
      }
      if (seen.has(name)) {
        toast.error(`Duplicate env var name "${name}"`);
        return null;
      }
      seen.add(name);
      if (!r.fromSecret && r.value === "") continue;
      cleaned.push(toEnvVar(r));
    }
    return cleaned;
  };

  const [pendingPayload, setPendingPayload] = useState<KusoEnvVar[] | null>(null);
  const diffEntries = useMemo<DiffEntry[]>(() => {
    if (!pendingPayload) return [];
    const beforeMap = new Map<string, string>();
    for (const v of env.data?.envVars ?? []) {
      if (!v.name) continue;
      // Run the server's raw env var through the same reversal the
      // editor applies on read (toRow reverses ${{ addon.KEY }} secret
      // refs and ${{ svc.URL }} resolved DNS literals; toEnvVar maps
      // it back to a KusoEnvVar). Without this the "before" side shows
      // the raw resolved form (<secret> / in-cluster DNS) while the
      // "after" side shows the ${{ }} ref form — so every untouched
      // reference env var falsely appears as a change.
      beforeMap.set(v.name, formatEnvForDiff(toEnvVar(toRow(v, project, addonByConn))));
    }
    const afterMap = new Map<string, string>();
    for (const v of pendingPayload) {
      if (!v.name) continue;
      afterMap.set(v.name, formatEnvForDiff(v));
    }
    const keys = new Set([...beforeMap.keys(), ...afterMap.keys()]);
    const out: DiffEntry[] = [];
    for (const k of keys) {
      const b = beforeMap.get(k);
      const a = afterMap.get(k);
      if (b === a) continue;
      out.push({ field: k, before: b, after: a });
    }
    out.sort((x, y) => x.field.localeCompare(y.field));
    return out;
  }, [pendingPayload, env.data, project, addonByConn]);

  const save = () => {
    const cleaned = cleanRows();
    if (cleaned == null) return;
    // No effective changes — fast-path: just clear dirty without
    // round-tripping. Saves a network call and a flash of the modal.
    setPendingPayload(cleaned);
  };
  // Revert to the last server-known row set. Used by the overlay
  // SaveBar's Discard button + ESC-prompt confirmation.
  const discard = () => {
    setRows(baselineFromRows.current);
    setDirty(false);
  };
  // Re-point the refs every render so the overlay hook fires the
  // latest closure (with the latest `rows`).
  saveRef.current = save;
  discardRef.current = discard;

  const applyPending = async () => {
    if (!pendingPayload) return;
    try {
      await setEnv.mutateAsync(pendingPayload);
      toast.success("Env vars saved");
      setDirty(false);
      setSavedAt(Date.now());
      setPendingPayload(null);
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

      {/* Status banner — single source of truth derived from kube
          timestamps. Replaces the previous 3-state chip
          (rolling/stale/saved) which flickered between states during
          a rollout and disagreed with itself across refresh.
          See driftBanner() for the state machine. */}
      <DriftBanner drift={drift.data} stickySaved={stickySaved} ageSec={ageSec} />
      {/* Hide the legacy `now` re-render when no banner is up — the
          interval still ticks for the sticky window. */}
      <span className="sr-only">{now}</span>

      {/* Detected env vars — names kuso noticed are referenced by
          the source repo (build-time scan) or that crashed the pod
          at runtime (log shipper hints), but aren't set here yet.
          One-click add seeds an empty row the user fills with the
          actual value. The banner stays out of the way unless we
          have something to suggest. */}
      <DetectedEnvBanner
        detected={detected.data}
        rows={rows}
        onAdd={(names) => {
          // Append empty rows for each missing name. dedupe against
          // existing entries (case-insensitive — env vars are
          // canonically uppercase but humans type sloppily).
          const existing = new Set(rows.map((r) => r.name.toUpperCase()));
          const adds: Row[] = [];
          for (const n of names) {
            if (!existing.has(n.toUpperCase())) {
              adds.push({ id: rid(), name: n, value: "", fromSecret: false, visible: false });
              existing.add(n.toUpperCase());
            }
          }
          if (adds.length) {
            setRows((prev) => [...prev, ...adds]);
            setDirty(true);
          }
        }}
      />

      {mode === "rows" ? (
        <div className="space-y-1.5">
          {rows.length === 0 && (
            <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-6 text-center text-xs text-[var(--text-tertiary)]">
              No env vars. Click <span className="font-mono">Add</span> or paste a{" "}
              <span className="font-mono">.env</span> file via Bulk mode.
            </p>
          )}
          {rows.map((r, i) => (
            // Stable per-row id so typing into the name field doesn't
            // change the key (which would unmount the row and steal
            // focus from the input — every keystroke blurred). Also
            // keeps deletes from the middle correct since the survivor
            // keeps its id.
            <div
              key={r.id}
              className="grid grid-cols-[180px_1fr_auto_auto_auto] items-center gap-1.5"
            >
              <div className="flex flex-col gap-0.5">
                <Input
                  placeholder="KEY"
                  value={r.name}
                  onChange={(e) => update(i, { name: e.target.value })}
                  className={cn(
                    "h-8 font-mono text-[12px]",
                    reservedEnvWarning(r.name) && "border-amber-500/60",
                  )}
                  disabled={r.fromSecret}
                  spellCheck={false}
                />
                {reservedEnvWarning(r.name) && (
                  <span className="font-mono text-[10px] text-amber-400">
                    {reservedEnvWarning(r.name)}
                  </span>
                )}
              </div>
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
        {!canWrite && (
          <span
            className="font-mono text-[10px] text-[var(--text-tertiary)]"
            title="secrets:write permission required"
          >
            read-only
          </span>
        )}
        {dirty && canWrite && (
          <span
            className="inline-flex items-center gap-1 rounded-md border border-amber-500/40 bg-amber-500/10 px-1.5 py-0.5 font-mono text-[10px] text-amber-200"
            title="Saving env-var changes triggers a rolling restart of the deployment."
          >
            redeploys on save
          </span>
        )}
        {/* Save / Discard moved to the unified SaveBar at the bottom
            of ServiceOverlay (U-P0-D). The bar sits above the panel
            scroll so it's always reachable on long env-var lists,
            and the keyboard shortcut (⌘S) wires to it directly. */}
      </div>
      <DiffConfirmDialog
        open={pendingPayload != null}
        title="Apply env-var changes?"
        description="Saving will roll a fresh pod with the updated environment. The current pod stays up until the new one is Ready."
        entries={diffEntries}
        confirmLabel="Apply & redeploy"
        confirming={setEnv.isPending}
        onCancel={() => setPendingPayload(null)}
        onConfirm={applyPending}
      />
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

// ServiceRefRow surfaces the canonical synthetic keys for a service.
// INTERNAL_URL = in-cluster DNS (backend↔backend); PUBLIC_URL =
// externally-reachable domain (frontend in a browser → backend);
// PORT = the bare container port for callers that already have the
// host (sidecar configs, healthchecks, etc.). URL/HOST still work as
// refs for back-compat but aren't surfaced here — they duplicate the
// matched _URL pair without adding signal.
function ServiceRefRow({ service, onPick }: { service: string; onPick: (ref: string) => void }) {
  const KEYS = ["INTERNAL_URL", "PUBLIC_URL", "PORT"];
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
// DetectedEnvBanner shows the merged build-scan + crash-hint set,
// minus anything already in the editor's rows. Two visual states:
//
//   - Crash-hint present (a recent pod log matched the missing-env
//     regex): orange-bordered alert with the var name + the log line
//     that triggered, plus "Add" to seed an empty row.
//   - Build-scan only (.env.example or source grep referenced X but
//     it isn't set): muted suggestion strip with all candidates as
//     chips and a single "Add all missing" affordance.
//
// Hidden when both lists are empty or every detected name is already
// in the rows. Clicking Add doesn't save — the row is added in
// dirty state, the user fills the value, then hits the existing Save.
function DetectedEnvBanner({
  detected,
  rows,
  onAdd,
}: {
  detected: DetectedEnv | undefined;
  rows: Row[];
  onAdd: (names: string[]) => void;
}) {
  if (!detected) return null;
  const haveSet = new Set(rows.map((r) => r.name.toUpperCase()).filter(Boolean));
  const missing = (detected.names ?? []).filter(
    (n) => n && !haveSet.has(n.toUpperCase()),
  );
  const hints = (detected.hints ?? []).filter(
    (h) => h.name && !haveSet.has(h.name.toUpperCase()),
  );
  if (missing.length === 0 && hints.length === 0) return null;

  return (
    <div className="space-y-2">
      {hints.length > 0 && (
        <div className="rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-[12px]">
          <div className="flex items-start gap-2">
            <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-400" />
            <div className="flex-1 space-y-1.5">
              <div className="text-amber-200">
                Recent crash mentions{" "}
                {hints.length === 1 ? "an env var" : `${hints.length} env vars`} that
                {hints.length === 1 ? " isn't" : " aren't"} set:
              </div>
              <div className="space-y-1">
                {hints.slice(0, 5).map((h) => (
                  <div
                    key={h.name}
                    className="flex items-center justify-between gap-2 rounded bg-[var(--bg-tertiary)]/40 px-2 py-1"
                  >
                    <div className="min-w-0 flex-1">
                      <div className="font-mono text-[11px] text-amber-300">{h.name}</div>
                      <div className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">
                        {h.lastLine}
                      </div>
                    </div>
                    <button
                      type="button"
                      onClick={() => onAdd([h.name])}
                      className="inline-flex shrink-0 items-center gap-1 rounded border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] text-amber-200 hover:bg-amber-500/20"
                    >
                      <Plus className="h-3 w-3" />
                      Add
                    </button>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>
      )}
      {missing.length > 0 && (
        <div className="rounded-md border border-dashed border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2 text-[12px]">
          <div className="flex items-start gap-2">
            <Wand2 className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
            <div className="flex-1">
              <div className="mb-1 flex items-center justify-between gap-2">
                <span className="text-[var(--text-secondary)]">
                  {missing.length} env{" "}
                  {missing.length === 1 ? "var" : "vars"} referenced in source but not set
                </span>
                <button
                  type="button"
                  onClick={() => onAdd(missing)}
                  className="inline-flex shrink-0 items-center gap-1 rounded border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-2 py-0.5 text-[10px] text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
                >
                  <Plus className="h-3 w-3" />
                  Add all
                </button>
              </div>
              <div className="flex flex-wrap gap-1">
                {missing.map((n) => (
                  <button
                    key={n}
                    type="button"
                    onClick={() => onAdd([n])}
                    className="rounded bg-[var(--bg-tertiary)]/60 px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                  >
                    {n}
                  </button>
                ))}
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function InheritedSection({ project }: { project: string }) {
  // Use the shared api() wrapper for the 401 path so an expired
  // session bounces to /login. 403 (admin-only endpoints) still
  // soft-falls to the empty shape since non-admins legitimately
  // see no instance-level inherited vars.
  const projectKeys = useQuery<{ keys: string[] }>({
    queryKey: ["projects", project, "shared-secrets"],
    queryFn: () =>
      api<{ keys: string[] }>(
        `/api/projects/${encodeURIComponent(project)}/shared-secrets`,
      ).catch((e: unknown) =>
        e instanceof ApiError && e.status === 403 ? { keys: [] } : Promise.reject(e),
      ),
    staleTime: 60_000,
  });
  const instanceKeys = useQuery<{ keys: string[] }>({
    queryKey: ["instance-secrets"],
    queryFn: () =>
      api<{ keys: string[] }>(`/api/instance-secrets`).catch((e: unknown) =>
        // Non-admins get 403; treat as empty rather than error.
        e instanceof ApiError && e.status === 403 ? { keys: [] } : Promise.reject(e),
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

// DriftBanner picks one banner state from the drift report instead of
// the previous flicker-prone 3-state chip. Decision tree, in order:
//
//   1. helmError != ""   → red "deploy failed" with the error message
//   2. lastSpecMutation set AND lastRolloutAt set AND
//      rolloutDelta >= 0 AND age < 60s → green "saved Ns ago, rolled
//      out Ms after save". This is the success confirmation.
//   3. rolloutPending OR podsStale.length > 0 → blue "rolling out N
//      seconds in (pod hasn't caught up)". One signal, not two.
//   4. else → null
//
// All durations are computed from server timestamps so a hard refresh
// keeps the same banner.
function DriftBanner({
  drift,
  stickySaved,
  ageSec,
}: {
  drift: import("@/features/services/api").DriftReport | undefined;
  stickySaved: boolean;
  ageSec: number;
}) {
  if (!drift) return null;
  const helmErr = drift.helmError?.trim();
  if (helmErr) {
    return (
      <div className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-[12px]">
        <div className="flex items-start gap-2">
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-red-400" />
          <div className="flex-1">
            <div className="font-medium text-red-200">Deploy failed</div>
            <div className="mt-1 break-words font-mono text-[11px] text-[var(--text-tertiary)]">
              {helmErr}
            </div>
          </div>
        </div>
      </div>
    );
  }
  const editedAt = drift.lastSpecMutation ? Date.parse(drift.lastSpecMutation) : NaN;
  const rolledAt = drift.lastRolloutAt ? Date.parse(drift.lastRolloutAt) : NaN;
  const rolling = drift.rolloutPending || (drift.podsStale?.length ?? 0) > 0;
  const now = Date.now();
  if (rolling) {
    const ago = Number.isFinite(editedAt) ? Math.max(0, Math.round((now - editedAt) / 1000)) : null;
    return (
      <div className="rounded-md border border-blue-500/40 bg-blue-500/5 px-3 py-2 text-[12px]">
        <div className="flex items-start gap-2">
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-blue-400" />
          <div className="flex-1 text-blue-200">
            Rolling out{ago != null ? ` (saved ${ago}s ago)` : "…"}. The new pod
            won&apos;t serve traffic until it&apos;s Ready.
          </div>
        </div>
      </div>
    );
  }
  if (Number.isFinite(editedAt) && Number.isFinite(rolledAt) && rolledAt >= editedAt) {
    const sinceSave = Math.max(0, Math.round((now - editedAt) / 1000));
    if (sinceSave < 120) {
      const rolloutDelta = Math.max(0, Math.round((rolledAt - editedAt) / 1000));
      return (
        <div className="rounded-md border border-emerald-500/40 bg-emerald-500/5 px-3 py-2 text-[12px]">
          <div className="flex items-start gap-2">
            <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-emerald-400" />
            <div className="text-emerald-200">
              Saved {sinceSave}s ago — pod started {rolloutDelta}s after save.
            </div>
          </div>
        </div>
      );
    }
  }
  if (stickySaved && ageSec < 5) {
    return (
      <div className="rounded-md border border-emerald-500/40 bg-emerald-500/5 px-3 py-2 text-[12px]">
        <div className="flex items-start gap-2">
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-emerald-400" />
          <div className="text-emerald-200">
            Saved. Waiting for the rollout to start…
          </div>
        </div>
      </div>
    );
  }
  return null;
}

// rowsShallowEqual is a cheap diff for the conflict detector — same
// length, same name/value/fromSecret tuple per row in order. Catches
// the common cases (added var, edited value, removed var) without
// pulling in lodash.isEqual.
function rowsShallowEqual(a: Row[], b: Row[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i].name !== b[i].name) return false;
    if (a[i].value !== b[i].value) return false;
    if (a[i].fromSecret !== b[i].fromSecret) return false;
  }
  return true;
}
