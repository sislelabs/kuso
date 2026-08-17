"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useOverlayDirty } from "@/components/service/ServiceOverlay";
import { Button } from "@/components/ui/button";
import { DiffConfirmDialog, type DiffEntry } from "@/components/shared/DiffConfirmDialog";
import { serviceBlast } from "@/lib/blast-radius";
import { Input } from "@/components/ui/input";
import { Trash2, Plus, Eye, EyeOff, FileText, List, Link2, AlertCircle, Wand2 } from "lucide-react";
import { useServiceEnv, useDetectedEnv, useDrift } from "@/features/services";
import type { DetectedEnv } from "@/features/services/api";
import { listAddonSecretKeys, setServiceEnvValue, unsetServiceEnvVar } from "@/features/services/api";
import { useProject, useAddons } from "@/features/projects";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCanOnProject, Perms } from "@/features/auth";
import { api, ApiError } from "@/lib/api-client";
import {
  ENV_MASK_SENTINEL,
  ENV_NAME_RE,
  addonShortByConnSecret,
  dotenvToRows,
  reservedEnvWarning,
  rid,
  rowDiffLabel,
  rowsShallowEqual,
  rowsToDotenv,
  toRow,
  type Row,
} from "@/components/service/envVarTransforms";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

// Row + the pure transform helpers (toRow, literalToRef, the dotenv
// serializers, ...) live in ./envVarTransforms so they're unit-testable
// without rendering this component. This file keeps only the stateful
// editor.

type Mode = "rows" | "bulk";

// PendingSave is the payload the confirm dialog applies as idempotent
// per-key operations (no wholesale bulk overwrite → no partial-save gap):
//   1. valueWrites — every create/change, written per-key via the unified
//      {value, auto:true} PUT so the SERVER decides storage (CR literal /
//      managed secret / secretKeyRef) and clears any stale prior form.
//   2. deletes — every removed row (any form). The server's UnsetEnvVar
//      removes a literal, a secretKeyRef, or a managed-secret key alike, so
//      one per-key DELETE covers all removals.
// Unchanged opaque secret-ref rows are in NEITHER channel — they're left
// exactly as they are.
interface PendingSave {
  valueWrites: { name: string; value: string }[];
  deletes: string[];
}

export function EnvVarsEditor({
  project,
  service,
  env: envScope,
}: {
  project: string;
  service: string;
  // env-group scope the parent overlay is showing. Used to fetch
  // per-env Secret keys so DetectedEnvBanner doesn't mark them as
  // "missing" (they're mounted on the pod via envFromSecrets) and
  // so the new InheritedPerEnvSection can surface them.
  env: string;
}) {
  const qc = useQueryClient();
  // reveal drives the ?reveal=true env read: the server resolves every
  // value (managed secrets + addon/shared secretKeyRefs) to plaintext,
  // admin-only. Flipped on the first time the user clicks the eye on a
  // secret-backed row whose value isn't loaded yet. A non-admin flipping
  // it still gets masked values back (server-gated), so it's harmless.
  const [reveal, setReveal] = useState(false);
  const env = useServiceEnv(project, service, reveal);
  // The save runs as per-key upserts/deletes (see applyPending), not a
  // single mutation, so we drive the saving/error UI from local state
  // rather than a mutation object.
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | undefined>(undefined);
  const detected = useDetectedEnv(project, service);
  const drift = useDrift(project, service);
  const addons = useAddons(project);
  // Per-env Secret keys. Powers the new "From per-env secret" group
  // in InheritedSection AND the haveSet filter in DetectedEnvBanner
  // so keys actually mounted (just via per-env Secret, not via
  // shared subscription or spec.envVars) don't get flagged as
  // "referenced in source but not set". Returns {keys: [...], env}.
  const perEnvSecrets = useQuery<{ keys: string[]; env: string | null }>({
    queryKey: ["projects", project, "services", service, "secrets", envScope],
    queryFn: () =>
      api<{ keys: string[]; env: string | null }>(
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/secrets?env=${encodeURIComponent(envScope)}`,
      ).catch((e: unknown) =>
        e instanceof ApiError && e.status === 403
          ? { keys: [], env: envScope }
          : Promise.reject(e),
      ),
    staleTime: 15_000,
  });
  // Shared-env subscription. Lifted to the editor level so both
  // InheritedSection (chip rendering) and DetectedEnvBanner
  // (haveSet inclusion) read from the same cache — InheritedSection
  // already fetches the same key internally, but DetectedEnvBanner
  // had no signal that a subscribed key was "set". Without this
  // any subscribed key got flagged as "referenced in source but not
  // set" even though the chip above it showed it as subscribed.
  const sharedSub = useQuery<SubscribableShape>({
    queryKey: ["projects", project, "services", service, "shared-env-keys"],
    queryFn: () =>
      api<SubscribableShape>(
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/shared-env-keys`,
      ).catch((e: unknown) =>
        e instanceof ApiError && e.status === 403
          ? { subscribed: [], sources: [] }
          : Promise.reject(e),
      ),
    staleTime: 30_000,
  });
  // Memoised so the toRow effect below only re-runs when the addon set
  // (or its connectionSecret status fields) actually changes. Without
  // memo, every re-render rebuilds the map and the effect's dep array
  // would point at a fresh reference each time.
  const addonByConn = useMemo(
    () => addonShortByConnSecret(addons.data ?? [], project),
    [addons.data, project]
  );
  // Known env scopes for this project (production, staging, preview-pr-N,
  // ...) so literalToRef can strip the env-scope segment a resolved
  // service URL carries (`<project>-<svc>-<scope>...`) back to the short
  // service name. Without this the read→save round-trip corrupts
  // ${{ svc.URL }} into ${{ svc-production.URL }}.
  const proj = useProject(project);
  const knownScopes = useMemo(() => {
    const envs =
      (proj.data as { environments?: { metadata?: { labels?: Record<string, string> } }[] } | undefined)
        ?.environments ?? [];
    const scopes = new Set<string>(["production"]);
    for (const e of envs) {
      const scope = e.metadata?.labels?.["kuso.sislelabs.com/env"];
      if (scope) scopes.add(scope);
    }
    // Longest-first so "preview-pr-7" is tried before "pr" etc.
    return Array.from(scopes).sort((a, b) => b.length - a.length);
  }, [proj.data]);
  // secrets:write gates the Save + the per-row destructive
  // affordances.
  //
  // role-system v2: the server MASKS env values for non-admins (returns
  // a "••••••••" sentinel + masked:true). If we let a masked session
  // save, the sentinel would overwrite the real values — so masked ⇒
  // read-only. Editors who need to CHANGE a value use the per-key blind
  // set (kuso env set), not this whole-list editor.
  const masked = env.data?.masked ?? false;
  const canWrite = useCanOnProject(project, Perms.SecretsWrite) && !masked;
  const [rows, setRows] = useState<Row[]>([]);
  const [dirty, setDirty] = useState(false);
  // Type-ahead: when the user types "${{" into a row's value, open
  // the ReferencePicker for that row so they can pick a service/addon
  // without hunting for the icon button. One row at a time; the
  // picker resets the latch via onForceCloseConsumed when it closes
  // so a second edit on the same row can re-trigger.
  const [pickerOpenForIndex, setPickerOpenForIndex] = useState<number | null>(null);
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
  // saveError stays set after a failed save until the next attempt
  // next mutate() resets it; surface it through the SaveBar so the
  // user sees a sticky reason for the failure (instead of a 4s toast
  // that disappears while they're still reading it).
  useOverlayDirty("variables", dirty && canWrite, {
    onSave: () => saveRef.current(),
    onDiscard: () => discardRef.current(),
    saving,
    saveError,
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
      .map((v) => toRow(v, project, addonByConn, knownScopes))
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
  }, [env.data, addonByConn, project, dirty, conflictNotified, knownScopes]);

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
    // Opaque secret-ref rows AND secret-backed rows whose plaintext isn't
    // revealed are unrepresentable in the dotenv textarea (there's no
    // value to round-trip), so carry them across a bulk edit untouched.
    const secrets = rows.filter((r) => r.fromSecret || (r.secretBacked && r.value === ""));
    setRows(dotenvToRows(text, secrets));
    setDirty(true);
  };

  const update = (idx: number, patch: Partial<Row>) => {
    // Type-ahead trigger: detect the moment the user just typed
    // "${{" into a value (not present in the prior value, present
    // now). Opens the ReferencePicker so they can pick a target
    // without reaching for the icon button. Comparing against the
    // prior value rather than just checking the new value means
    // editing an existing ${{ ref }} doesn't re-open the picker on
    // every keystroke.
    if (typeof patch.value === "string") {
      const prevValue = rows[idx]?.value ?? "";
      if (!prevValue.includes("${{") && patch.value.includes("${{")) {
        setPickerOpenForIndex(idx);
      }
    }
    setRows((prev) => prev.map((r, i) => (i === idx ? { ...r, ...patch } : r)));
    // Only mark dirty when an actually-persisted field changed. The
    // visible flag is a UI-only "show/hide value" toggle; clicking
    // the eye should not pop the save bar. Keys to ignore: `visible`,
    // `id` (internal row identity).
    const persistedKeys = Object.keys(patch).filter(
      (k) => k !== "visible" && k !== "id",
    );
    if (persistedKeys.length > 0) {
      setDirty(true);
    }
  };
  const remove = (idx: number) => {
    setRows((prev) => prev.filter((_, i) => i !== idx));
    setDirty(true);
  };
  const add = () => {
    setRows((prev) => [...prev, { id: rid(), name: "", value: "", fromSecret: false, secretBacked: false, visible: true }]);
    setDirty(true);
  };

  // Two-step save under the "one secret primitive" model. cleanRows()
  // validates + dedups, splitting the result into:
  //   1. envVars — the opaque secret-ref rows (fromSecret) the editor
  //      can't represent as a typed value. Written via the bulk POST /env
  //      (wholesale overwrite of spec.envVars). Running it first drops any
  //      removed literal and clears the CR of everything the per-key
  //      writes are about to re-add.
  //   2. valueWrites — every typed value/ref the user entered, written
  //      per-key via {value, auto:true} so the SERVER decides storage.
  // Returns null when validation toast'd.
  const cleanRows = (): PendingSave | null => {
    // Baseline value per name so we can tell an untouched managed-secret
    // row (whose real plaintext may have been revealed into `value`) from
    // one the user actually edited. Re-writing an unrevealed/unchanged
    // managed secret is pointless and would re-store revealed plaintext.
    const baselineValue = new Map<string, string>();
    for (const b of baselineFromRows.current) {
      const n = b.name.trim();
      if (n) baselineValue.set(n, b.value);
    }
    const seen = new Set<string>();
    const valueWrites: { name: string; value: string }[] = [];
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
      // Opaque secret-ref rows (fromSecret) can only be kept or removed —
      // their inputs are locked, so an unchanged one needs NO write. It is
      // NOT re-emitted through a wholesale bulk overwrite (that path
      // introduced a save-atomicity gap: it cleared typed vars first, then
      // re-added them per-key, so a mid-save failure left the CR partial).
      // Removal is handled by the deletes channel below.
      if (r.fromSecret) continue;
      // Mask-sentinel guard: never write the masked "••••••••" placeholder
      // back over the real value. Masked sessions are already read-only
      // (canWrite=false), but defend in depth here too — a revealed row
      // whose value somehow still holds the sentinel is skipped, not saved.
      if (r.value === ENV_MASK_SENTINEL) continue;
      // Empty value: skip. For a brand-new row that's a no-op; for a
      // secret-backed row the user opened but didn't retype, skipping
      // leaves the existing stored value untouched (we don't re-write it).
      if (r.value === "") continue;
      // Secret-backed value the user didn't change (a reveal populates
      // `value` with the current plaintext, so an unedited revealed row
      // matches its baseline): skip so we don't churn the Secret or
      // re-store revealed plaintext. Everything else is a real
      // create/change → a per-key {value, auto:true} upsert (server decides
      // storage AND clears any stale prior form of the same name).
      if (r.managed && r.value === baselineValue.get(name)) continue;
      valueWrites.push({ name, value: r.value });
    }
    // Every removed row goes through an explicit per-key DELETE — the
    // server's UnsetEnvVar removes any form (literal, secretKeyRef, or
    // managed-secret key). This replaces the old wholesale bulk overwrite
    // that dropped removals by omission; per-key deletes are idempotent
    // and don't clear untouched vars. A DELETE on an already-gone name is
    // tolerated (404 → treated as success in applyPending).
    const present = new Set(rows.map((r) => r.name.trim()).filter(Boolean));
    const deletes: string[] = [];
    for (const b of baselineFromRows.current) {
      const name = b.name.trim();
      if (name && !present.has(name)) deletes.push(name);
    }
    return { valueWrites, deletes };
  };

  const [pendingPayload, setPendingPayload] = useState<PendingSave | null>(null);
  const diffEntries = useMemo<DiffEntry[]>(() => {
    if (!pendingPayload) return [];
    // Diff the server-known baseline rows against the current rows, keyed
    // by name. Working row-to-row (rather than payload-to-server) keeps
    // untouched secret-backed rows — whose blank value is skipped on save
    // and left in their Secret — from showing as spurious removals, and
    // renders every value through the same masking so no plaintext leaks.
    const beforeMap = new Map<string, string>();
    for (const r of baselineFromRows.current) {
      const name = r.name.trim();
      if (!name) continue;
      beforeMap.set(name, rowDiffLabel(r));
    }
    const afterMap = new Map<string, string>();
    for (const r of rows) {
      const name = r.name.trim();
      if (!name || r.value === ENV_MASK_SENTINEL) continue;
      // Untouched secret-backed row (blank value): not being rewritten,
      // and its stored value survives — so treat it as unchanged by
      // carrying the "before" label through rather than showing a diff.
      if (r.value === "" && r.secretBacked) {
        if (beforeMap.has(name)) afterMap.set(name, beforeMap.get(name)!);
        continue;
      }
      afterMap.set(name, rowDiffLabel(r));
    }
    const keys = new Set([...beforeMap.keys(), ...afterMap.keys()]);
    const out: DiffEntry[] = [];
    // Every env-var change re-renders the Deployment → rolling
    // restart. Surface that blast radius once, on the first row.
    const envWarning = serviceBlast("envVars") ?? undefined;
    let first = true;
    for (const k of keys) {
      const b = beforeMap.get(k);
      const a = afterMap.get(k);
      if (b === a) continue;
      out.push({ field: k, before: b, after: a, warning: first ? envWarning : undefined });
      first = false;
    }
    out.sort((x, y) => x.field.localeCompare(y.field));
    return out;
  }, [pendingPayload, rows]);

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
    const { valueWrites, deletes } = pendingPayload;
    setSaving(true);
    setSaveError(undefined);
    try {
      // The whole save is expressed as idempotent per-key operations — no
      // wholesale bulk overwrite, so there is no window where the CR is
      // partially cleared (the earlier bulk-first-then-per-key approach had
      // that atomicity gap). Upserts run before deletes; a failure surfaces
      // the offending key and leaves already-applied keys correct.
      //
      // 1. Every create/change → {value, auto:true}. The server decides
      //    storage (CR literal / managed secret / secretKeyRef) and clears
      //    any stale prior form of the same name.
      for (const w of valueWrites) {
        await setServiceEnvValue(project, service, w.name, w.value);
      }
      // 2. Every removed row → DELETE. UnsetEnvVar removes any form. A
      //    404 (already gone) is fine — treat it as success.
      for (const name of deletes) {
        try {
          await unsetServiceEnvVar(project, service, name);
        } catch (e) {
          if (!(e instanceof ApiError && e.status === 404)) throw e;
        }
      }
      // Invalidate the env (incl. the reveal variant) + drift queries to
      // refetch current values and surface the rollout banner.
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "env"] });
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "drift"] });
      toast.success("Env vars saved");
      setDirty(false);
      setSavedAt(Date.now());
      setPendingPayload(null);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "Failed to save env vars";
      setSaveError(msg);
      toast.error(msg);
    } finally {
      setSaving(false);
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

      {/* Masked-values banner — non-admins (viewer/editor) can see which
          env keys exist but not their values, which the server replaces
          with a sentinel. The editor is read-only in this mode so the
          sentinel can't be saved over the real values. */}
      {masked && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-[11px] leading-relaxed text-amber-300/90">
          Env var <span className="font-semibold">values are hidden</span> — reading them
          requires the admin role. You can see the keys but the values are masked, and this
          editor is read-only. To change a value without seeing the current one, an admin can
          grant access or use a blind per-key set.
        </div>
      )}

      {/* Inherited env vars — keys that flow in from project-level
          and instance-level shared secrets. Read-only display with a
          link to the place to edit. Helps users understand WHY their
          service has DATABASE_URL or SENTRY_DSN without having defined
          it locally. Show even when empty so the affordance is
          discoverable on day 1. */}
      <InheritedSection project={project} service={service} />

      {/* Per-env Secrets (kuso secret set --env <env>) were
          previously surfaced here as a separate section, but they're
          a low-level escape hatch — the primary primitive for any
          env var, sensitive or not, is the row editor below
          (kuso env set). We still consume the per-env Secret KEY
          list as an extraSetNames signal in DetectedEnvBanner so
          legacy entries don't get flagged as missing, but we no
          longer render them as a separate UI section. Users who
          want to view legacy per-env Secret keys can grep them via
          `kuso secret list <p> <s> --env <env>` from the CLI;
          everything new should go through the row editor. */}

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
        // Keys present in the per-env Secret OR in the shared
        // subscription count as "set" — both flow into the pod's
        // env via envFromSecrets / valueFrom, just not via the
        // editor's row list. Without merging both signals, every
        // subscribed shared key + every per-env NEXT_PUBLIC_* got
        // flagged as "referenced in source but not set" even when
        // the pod had them in process.env.
        extraSetNames={[
          ...(perEnvSecrets.data?.keys ?? []),
          ...(sharedSub.data?.subscribed ?? []),
        ]}
        onAdd={(names) => {
          // Append empty rows for each missing name. dedupe against
          // existing entries (case-insensitive — env vars are
          // canonically uppercase but humans type sloppily).
          const existing = new Set(rows.map((r) => r.name.toUpperCase()));
          const adds: Row[] = [];
          for (const n of names) {
            if (!existing.has(n.toUpperCase())) {
              adds.push({ id: rid(), name: n, value: "", fromSecret: false, secretBacked: false, visible: false });
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
            // Mobile (<sm): name + value stack full-width, the three
            // actions sit on their own row — a 180px name column plus
            // three buttons crushes the value field to ~100px at 375px.
            // At sm+ this collapses back to the exact desktop single-row
            // grid so nothing changes on a laptop.
            <div
              key={r.id}
              className={cn(
                // One row = one value. Three actions: 🔗 wire a ref,
                // 👁 reveal, 🗑 remove.
                "flex flex-col gap-1.5 rounded-md border p-1.5 sm:grid sm:grid-cols-[180px_1fr_auto_auto_auto] sm:items-center sm:rounded-none sm:border-0 sm:p-0",
                // Opaque secret-ref rows (fromSecret) get a subtle tint so
                // they read as "wired to a secret — edit with 🔗" vs a
                // typed value. On desktop the border collapses
                // (sm:border-0) so the placeholder carries the distinction.
                r.fromSecret
                  ? "border-amber-500/30 bg-amber-500/5"
                  : "border-[var(--border-subtle)]",
              )}
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
                  // A ref / secret-backed KEY comes from the server and can't
                  // be renamed here (a rename would mint a new key, not move
                  // the value). Brand-new rows are freely editable.
                  disabled={r.fromSecret || r.secretBacked}
                  spellCheck={false}
                />
                {reservedEnvWarning(r.name) && (
                  <span className="font-mono text-[10px] text-amber-400">
                    {reservedEnvWarning(r.name)}
                  </span>
                )}
              </div>
              <Input
                placeholder={
                  r.fromSecret
                    ? "→ secret ref (use 🔗 to change)"
                    : r.secretBacked
                      ? "••••• (type to set a new value)"
                      : "value or ${{ ref }}"
                }
                // Secret-backed values arrive blank/masked — show them only
                // when revealed (eye), otherwise as a password field.
                type={r.visible || r.fromSecret ? "text" : "password"}
                value={r.value}
                onChange={(e) => update(i, { value: e.target.value })}
                className="h-8 min-w-0 font-mono text-[12px]"
                // Opaque refs aren't type-editable — the value is a resolved
                // secret we can't render; the 🔗 picker re-wires them.
                disabled={r.fromSecret}
                spellCheck={false}
              />
              {/* On mobile the actions share one row (justify-end); on
                  desktop they are grid cells (contents unwraps this flex so
                  each button lands in its own column). */}
              <div className="flex items-center justify-end gap-1 sm:contents">
                <ReferencePicker
                  project={project}
                  excludeService={service}
                  // Picking a ref sets the value AND turns the row into an
                  // editable ${{ ref }} value — a fromSecret/secret-backed
                  // row becomes a plain editable ref the user can re-pick.
                  onPick={(ref) =>
                    update(i, {
                      value: ref,
                      visible: true,
                      fromSecret: false,
                      secretBacked: false,
                      origValueFrom: undefined,
                    })
                  }
                  forceOpen={pickerOpenForIndex === i}
                  onForceCloseConsumed={() => setPickerOpenForIndex(null)}
                />
                <button
                  type="button"
                  aria-label={r.visible ? "Hide" : "Show"}
                  onClick={() => {
                    // Reveal path: secret-backed / opaque-ref values aren't
                    // loaded on the default read. First time the user opens
                    // one, ask the server to resolve plaintext (?reveal=true,
                    // admin-only). Guard on !dirty so the reveal refetch
                    // doesn't collide with in-progress edits.
                    if (
                      !r.visible &&
                      (r.secretBacked || r.fromSecret) &&
                      r.value === "" &&
                      !reveal &&
                      !dirty
                    ) {
                      setReveal(true);
                    }
                    update(i, { visible: !r.visible });
                  }}
                  className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-30"
                >
                  {r.visible ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                </button>
                <button
                  type="button"
                  aria-label="Remove"
                  onClick={() => remove(i)}
                  className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400 disabled:opacity-30"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </div>
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
        confirming={saving}
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
  forceOpen,
  onForceCloseConsumed,
}: {
  project: string;
  excludeService: string;
  onPick: (ref: string) => void;
  disabled?: boolean;
  // forceOpen lets the parent (e.g. the value input's `${{ ` type-
  // ahead detector) open the picker programmatically. The picker
  // calls onForceCloseConsumed when the user closes it so the parent
  // can reset its internal "user just typed ${{" latch — otherwise
  // a second edit on the same row would re-open the picker forever.
  forceOpen?: boolean;
  onForceCloseConsumed?: () => void;
}) {
  const [open, setOpen] = useState(false);
  // Honour external open requests. When the parent flips forceOpen
  // true we open; the closing flow notifies the parent so it can flip
  // the trigger back off.
  useEffect(() => {
    if (forceOpen) setOpen(true);
  }, [forceOpen]);
  const close = useCallback(() => {
    setOpen(false);
    onForceCloseConsumed?.();
  }, [onForceCloseConsumed]);
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
            close();
          }}
          onClose={close}
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
  extraSetNames,
  onAdd,
}: {
  detected: DetectedEnv | undefined;
  rows: Row[];
  // Additional key names that count as "satisfied" but live OUTSIDE
  // the editor's row list — typically the keys in a per-env Secret
  // mounted via envFromSecrets. The editor's rows + extraSetNames
  // together form the full "is this key actually set on the pod?"
  // signal.
  extraSetNames?: string[];
  onAdd: (names: string[]) => void;
}) {
  if (!detected) return null;
  const haveSet = new Set(rows.map((r) => r.name.toUpperCase()).filter(Boolean));
  for (const n of extraSetNames ?? []) {
    if (n) haveSet.add(n.toUpperCase());
  }
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

interface SubscribableShape {
  subscribed: string[];
  sources: { secret: string; keys: string[] }[];
}

function InheritedSection({ project, service }: { project: string; service: string }) {
  const qc = useQueryClient();
  const sub = useQuery<SubscribableShape>({
    queryKey: ["projects", project, "services", service, "shared-env-keys"],
    queryFn: () =>
      api<SubscribableShape>(
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/shared-env-keys`,
      ).catch((e: unknown) =>
        e instanceof ApiError && e.status === 403
          ? { subscribed: [], sources: [] }
          : Promise.reject(e),
      ),
    staleTime: 30_000,
  });
  const mut = useMutation({
    // api() stringifies opts.body itself — pass the object, not a
    // JSON string. Double-stringifying produced `"{\"keys\":[...]}"`
    // which the server rejected as malformed JSON (400), making the
    // chip clicks silently no-op in the UI.
    mutationFn: (keys: string[]) =>
      api<unknown>(
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/shared-env-keys`,
        { method: "PUT", body: { keys } },
      ),
    onSuccess: () =>
      qc.invalidateQueries({
        queryKey: ["projects", project, "services", service, "shared-env-keys"],
      }),
  });

  const sources = sub.data?.sources ?? [];
  const subscribed = new Set(sub.data?.subscribed ?? []);
  const totalAvailable = sources.reduce((n, s) => n + s.keys.length, 0);

  if (totalAvailable === 0) {
    return (
      <details className="group rounded-md border border-dashed border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-1.5">
        <summary className="cursor-pointer list-none font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]">
          inherited env vars · none available
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

  const toggle = (key: string) => {
    const next = new Set(subscribed);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    mut.mutate(Array.from(next).sort());
  };
  const addAll = () => {
    const all = sources.flatMap((s) => s.keys);
    mut.mutate(Array.from(new Set(all)).sort());
  };
  const clearAll = () => mut.mutate([]);

  return (
    <details
      open
      className="group rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40"
    >
      <summary className="flex cursor-pointer list-none items-center justify-between px-3 py-1.5 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        <span>
          inherited env vars · {subscribed.size}/{totalAvailable} subscribed
        </span>
        <span className="flex items-center gap-2">
          <button
            type="button"
            onClick={(e) => {
              e.preventDefault();
              addAll();
            }}
            className="rounded px-1.5 py-0.5 text-[10px] text-[var(--accent)] hover:bg-[var(--bg-tertiary)]"
            title="Subscribe to every available key"
          >
            +all
          </button>
          <button
            type="button"
            onClick={(e) => {
              e.preventDefault();
              clearAll();
            }}
            className="rounded px-1.5 py-0.5 text-[10px] text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
            title="Unsubscribe from every key"
          >
            clear
          </button>
          <span className="text-[var(--text-tertiary)] group-open:rotate-90 transition-transform">
            ›
          </span>
        </span>
      </summary>
      <div className="space-y-2 border-t border-[var(--border-subtle)] px-3 py-2">
        {sources.map((src) => (
          <SubscribableGroup
            key={src.secret}
            label={`from ${src.secret}`}
            editHref={
              src.secret === "kuso-instance-shared"
                ? "/settings/instance-secrets"
                : `/projects/${encodeURIComponent(project)}/settings`
            }
            keys={src.keys}
            effective={subscribed}
            onToggle={toggle}
            saving={mut.isPending}
          />
        ))}
      </div>
    </details>
  );
}


function SubscribableGroup({
  label,
  editHref,
  keys,
  effective,
  onToggle,
  saving,
}: {
  label: string;
  editHref: string;
  keys: string[];
  effective: Set<string>;
  onToggle: (key: string) => void;
  saving: boolean;
}) {
  return (
    <div>
      <div className="flex items-center justify-between">
        <p className="font-mono text-[10px] text-[var(--text-tertiary)]">{label}</p>
        <a
          href={editHref}
          className="font-mono text-[10px] text-[var(--accent)] hover:underline"
        >
          edit source →
        </a>
      </div>
      {keys.length === 0 ? (
        <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]/60">
          (none)
        </p>
      ) : (
        <div className="mt-1 flex flex-wrap gap-1">
          {[...keys].sort().map((k) => {
            const on = effective.has(k);
            return (
              <button
                key={k}
                type="button"
                disabled={saving}
                onClick={() => onToggle(k)}
                className={
                  on
                    ? "inline-flex items-center gap-1 rounded-md border border-[var(--accent)]/60 bg-[var(--accent)]/15 px-2 py-0.5 font-mono text-[10px] text-[var(--text-primary)] hover:bg-[var(--accent)]/25 disabled:opacity-60"
                    : "inline-flex items-center gap-1 rounded-md border border-dashed border-[var(--border-subtle)] bg-transparent px-2 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)] hover:border-[var(--text-secondary)] hover:text-[var(--text-secondary)] disabled:opacity-60"
                }
                title={on ? "Click to unsubscribe" : "Click to subscribe"}
              >
                <span>{on ? "✓" : "+"}</span>
                {k}
              </button>
            );
          })}
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
