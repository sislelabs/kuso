"use client";

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useOverlayDirty } from "@/components/service/ServiceOverlay";
import { DiffConfirmDialog, type DiffEntry } from "@/components/shared/DiffConfirmDialog";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { claimEscape } from "@/lib/escape-layer";
import { serviceBlast } from "@/lib/blast-radius";
import { Input } from "@/components/ui/input";
import { Check, ChevronRight, Eye, EyeOff, Link2, Lock, Search, Trash2 } from "lucide-react";
import { useServiceEnv, useServiceEnvOverrides, useDetectedEnv, useDrift } from "@/features/services";
import { listAddonSecretKeys, setServiceEnvValue, unsetServiceEnvVar } from "@/features/services/api";
import { setSharedSecret } from "@/features/project-secrets/api";
import { useProject, useAddons } from "@/features/projects";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCanOnProject, Perms } from "@/features/auth";
import { api, ApiError } from "@/lib/api-client";
import {
  ENV_MASK_SENTINEL,
  ENV_NAME_RE,
  addonShortByConnSecret,
  buildTimePrefix,
  dotenvToRows,
  envApplyFailureMessage,
  prefixGroups,
  reservedEnvWarning,
  rid,
  rowDiffLabel,
  rowsShallowEqual,
  toRow,
  type Row,
} from "@/components/service/envVarTransforms";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import {
  AddMenu,
  DeployStatus,
  INSTANCE_SHARED_SECRET,
  PasteEnvDialog,
  RowMenu,
  SharedPickerDialog,
  VarBadge,
  type SubscribableShape,
} from "@/components/service/EnvVarParts";

// Row + the pure transform helpers (toRow, literalToRef, the dotenv
// serializers, ...) live in ./envVarTransforms so they're unit-testable
// without rendering this component. This file keeps only the stateful
// editor.

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
  onAddGithubSignIn,
}: {
  project: string;
  service: string;
  // env-group scope the parent overlay is showing. Used to fetch
  // per-env Secret keys so DetectedEnvBanner doesn't mark them as
  // "missing" (they're mounted on the pod via envFromSecrets) and
  // so the new InheritedPerEnvSection can surface them.
  env: string;
  onAddGithubSignIn?: () => void;
}) {
  const qc = useQueryClient();
  // Always the reveal read: the server resolves every value to plaintext for
  // callers with secrets:read and masks it for everyone else, so rows load
  // with their real values and the eye only toggles masking.
  const env = useServiceEnv(project, service, true);
  const overrides = useServiceEnvOverrides(project, service, envScope);
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
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/shared-env-keys?reveal=true`,
      ).catch((e: unknown) =>
        e instanceof ApiError && e.status === 403
          ? { subscribed: [], sources: [] }
          : Promise.reject(e),
      ),
    staleTime: 30_000,
  });
  const [subscriptionSaving, setSubscriptionSaving] = useState(false);
  // Replaces the service's shared-secret subscription. Applied immediately,
  // not through the SaveBar: it's its own spec field with its own rollout.
  const [pendingUnsub, setPendingUnsub] = useState<string | null>(null);
  const setSubscription = async (keys: string[]) => {
    setSubscriptionSaving(true);
    try {
      await api<unknown>(
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/shared-env-keys`,
        { method: "PUT", body: { keys } },
      );
      await qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "shared-env-keys"] });
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "drift"] });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Couldn't update project secrets");
      throw e;
    } finally {
      setSubscriptionSaving(false);
    }
  };
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
  // UI-only state: which rows are open for editing, which values are
  // unmasked, which prefix groups are expanded.
  const [editing, setEditing] = useState<Set<string>>(new Set());
  const [shownValues, setShownValues] = useState<Set<string>>(new Set());
  const [revealAll, setRevealAll] = useState(false);
  const [openGroups, setOpenGroups] = useState<Set<string>>(new Set());
  const [search, setSearch] = useState("");
  const [sharedOpen, setSharedOpen] = useState(false);
  const [pasteOpen, setPasteOpen] = useState(false);

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
      // Only collapse open rows when the values actually changed; a poll
      // that merely re-derives addonByConn shouldn't close a row the user
      // just opened.
      if (!rowsShallowEqual(incoming, baselineFromRows.current)) setEditing(new Set());
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
    // Names present in the CURRENT rows — lets the rename guard below tell
    // a true rename (old name gone → it'll be deleted) from a row that just
    // happens to share a name with another still-present row.
    const present0 = new Set(rows.map((r) => r.name.trim()).filter(Boolean));
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
      //
      // EXCEPT when a secret-backed row was RENAMED: the old name is in
      // the deletes channel below, so skipping the write here would drop
      // the value entirely. We can't carry it over without the plaintext
      // (a blank row means it was never revealed), so refuse the save and
      // tell the user to reveal-and-retype rather than silently lose it.
      if (r.value === "") {
        const wasRenamed =
          r.secretBacked &&
          !!r.origName &&
          r.origName !== name &&
          !present0.has(r.origName);
        if (wasRenamed) {
          toast.error(
            `Reveal and re-enter the value for "${name}" — renaming a secret-backed var needs its value retyped.`,
          );
          return null;
        }
        continue;
      }
      // Secret-backed value the user didn't change (a reveal populates
      // `value` with the current plaintext, so an unedited revealed row
      // matches its baseline): skip so we don't churn the Secret or
      // re-store revealed plaintext. Everything else is a real
      // create/change → a per-key {value, auto:true} upsert (server decides
      // storage AND clears any stale prior form of the same name).
      if (r.managed && r.value === baselineValue.get(name)) continue;
      // Same for an untouched ADDON row: its value came from the addon's
      // conn Secret, so writing it back would mint a pointless override
      // that then shadows the addon forever (and goes stale the moment the
      // addon rotates its credentials). Only an actual edit overrides.
      if (r.addon && r.value === baselineValue.get(name)) continue;
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
    setEditing(new Set());
    setDirty(false);
  };
  // Re-point the refs every render so the overlay hook fires the
  // latest closure (with the latest `rows`).
  saveRef.current = save;
  discardRef.current = discard;

  const refreshAfterApply = () =>
    Promise.all([
      // env (incl. the reveal variant) + drift for the rollout banner, and
      // the describe payload the canvas derives edges from.
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "env"] }),
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "drift"] }),
      qc.invalidateQueries({ queryKey: ["projects", project], exact: true }),
    ]);

  const applyPending = async () => {
    if (!pendingPayload) return;
    const { valueWrites, deletes } = pendingPayload;
    setSaving(true);
    setSaveError(undefined);
    const applied: string[] = [];
    let current = "";
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
        current = w.name;
        await setServiceEnvValue(project, service, w.name, w.value);
        applied.push(w.name);
      }
      // 2. Every removed row → DELETE. UnsetEnvVar removes any form. A
      //    404 (already gone) is fine — treat it as success.
      for (const name of deletes) {
        current = name;
        try {
          await unsetServiceEnvVar(project, service, name);
        } catch (e) {
          if (!(e instanceof ApiError && e.status === 404)) throw e;
        }
        applied.push(name);
      }
      // Await the refetch before dropping dirty, or the sync effect
      // re-seeds the rows from the pre-save cache and they flash back.
      await refreshAfterApply();
      toast.success("Env vars saved");
      setDirty(false);
      setPendingPayload(null);
    } catch (e) {
      const reason = e instanceof Error ? e.message : "request failed";
      const msg = envApplyFailureMessage(applied, current, reason);
      setSaveError(msg);
      toast.error(msg);
      // Some keys may already be live: refresh so the editor and the
      // rollout banner reflect them. The local edits stay dirty so Save
      // retries (every write is idempotent); the refetch is ours, so
      // don't warn about "another edit".
      setConflictNotified(true);
      void refreshAfterApply();
    } finally {
      setSaving(false);
    }
  };

  // ── Display model ────────────────────────────────────────────────────
  // One list for everything the pod gets: the editable service vars (incl.
  // addon-injected keys), subscribed project/instance secrets, this env's
  // pinned overrides, and ghost rows for vars the code wants but nobody set.
  const sharedValues = sharedSub.data?.values;
  const sharedSourceOf = useMemo(() => {
    const m = new Map<string, "project" | "instance">();
    for (const src of sharedSub.data?.sources ?? []) {
      const kind = src.secret === INSTANCE_SHARED_SECRET ? "instance" : "project";
      // Project wins over instance on a key in both (same as the pod).
      for (const k of src.keys) if (!m.has(k) || kind === "project") m.set(k, kind);
    }
    return m;
  }, [sharedSub.data]);
  const overrideVars = useMemo(
    () => (overrides.data?.envVars ?? []).filter((v): v is typeof v & { name: string } => !!v.name),
    [overrides.data],
  );
  const overrideNames = useMemo(() => new Set(overrideVars.map((v) => v.name)), [overrideVars]);
  const rowNames = useMemo(() => new Set(rows.map((r) => r.name.trim()).filter(Boolean)), [rows]);
  const subscribed = useMemo(() => sharedSub.data?.subscribed ?? [], [sharedSub.data]);
  const baselineById = useMemo(() => {
    const m = new Map<string, Row>();
    for (const b of baselineFromRows.current) m.set(b.id, b);
    return m;
    // eslint-disable-next-line react-hooks/exhaustive-deps -- baseline changes with rows
  }, [rows]);
  const isChanged = (r: Row) => {
    const b = baselineById.get(r.id);
    return !b || b.name !== r.name || b.value !== r.value;
  };

  type Item =
    | { kind: "var"; name: string; row: Row; index: number }
    | { kind: "shared"; name: string; source: "project" | "instance" }
    | { kind: "override"; name: string; value: string; ref: boolean };

  const items = useMemo<Item[]>(() => {
    const out: Item[] = [];
    rows.forEach((row, index) => {
      if (row.name.trim()) out.push({ kind: "var", name: row.name, row, index });
    });
    for (const k of subscribed) {
      // A service var of the same name wins on the pod; the var row says so.
      if (!rowNames.has(k)) out.push({ kind: "shared", name: k, source: sharedSourceOf.get(k) ?? "project" });
    }
    for (const v of overrideVars) {
      out.push({ kind: "override", name: v.name, value: v.value ?? "", ref: !!v.valueFrom });
    }
    return out.sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
  }, [rows, subscribed, rowNames, sharedSourceOf, overrideVars]);

  // Rows added this session with no name yet render on top, in edit mode.
  const blankRows = rows
    .map((row, index) => ({ row, index }))
    .filter(({ row }) => !row.name.trim());

  const missing = useMemo(() => {
    const have = new Set<string>();
    for (const r of rows) if (r.name) have.add(r.name.toUpperCase());
    for (const k of subscribed) have.add(k.toUpperCase());
    for (const k of perEnvSecrets.data?.keys ?? []) have.add(k.toUpperCase());
    for (const k of overrideNames) have.add(k.toUpperCase());
    const out = new Map<string, string | undefined>();
    for (const h of detected.data?.hints ?? []) {
      if (h.name && !have.has(h.name.toUpperCase())) out.set(h.name, h.lastLine);
    }
    for (const n of detected.data?.names ?? []) {
      if (n && !have.has(n.toUpperCase()) && !out.has(n)) out.set(n, undefined);
    }
    return Array.from(out, ([name, crash]) => ({ name, crash }));
  }, [rows, subscribed, perEnvSecrets.data, overrideNames, detected.data]);

  const q = search.trim().toUpperCase();
  const shown = q ? items.filter((i) => i.name.toUpperCase().includes(q)) : items;
  const groupOf = useMemo(() => (q ? new Map<string, string>() : prefixGroups(items.map((i) => i.name))), [items, q]);

  const isVisible = (key: string) => revealAll || shownValues.has(key);
  const toggleVisible = (key: string) =>
    setShownValues((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  const copy = (value: string) => {
    void navigator.clipboard.writeText(value).then(
      () => toast.success("Copied"),
      () => toast.error("Couldn't copy"),
    );
  };
  const startEdit = (id: string) => setEditing((prev) => new Set(prev).add(id));
  const stopEdit = (id: string) =>
    setEditing((prev) => {
      const next = new Set(prev);
      next.delete(id);
      return next;
    });

  const addRow = (name = "") => {
    const row: Row = { id: rid(), name, value: "", fromSecret: false, secretBacked: false, visible: true };
    setRows((prev) => [...prev, row]);
    setEditing((prev) => new Set(prev).add(row.id));
    setDirty(true);
  };
  const applyPaste = (text: string) => {
    const parsed = dotenvToRows(text, []);
    if (parsed.length === 0) {
      toast.error("No KEY=value lines found");
      return;
    }
    setRows((prev) => {
      const next = [...prev];
      for (const p of parsed) {
        const i = next.findIndex((r) => r.name === p.name);
        if (i >= 0) {
          next[i] = { ...next[i], value: p.value, fromSecret: false, origValueFrom: undefined };
        } else {
          next.push({ ...p, visible: true });
        }
      }
      return next;
    });
    setDirty(true);
    setPasteOpen(false);
    toast(`${parsed.length} ${parsed.length === 1 ? "variable" : "variables"} staged. Save to apply.`);
  };

  // Promote a service var to a project secret: write it to <project>-shared,
  // subscribe this service, then drop the service var. The service var wins
  // on the pod until the last step, so there's no window without the value.
  const moveToShared = async (r: Row) => {
    if (dirty) {
      toast.error("Save or discard your changes first");
      return;
    }
    if (sharedSourceOf.get(r.name) === "project") {
      toast.error(`${r.name} already exists in project secrets`);
      return;
    }
    try {
      await setSharedSecret(project, { key: r.name, value: r.value });
      await setSubscription(Array.from(new Set([...subscribed, r.name])).sort());
      await unsetServiceEnvVar(project, service, r.name);
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "env"] });
      qc.invalidateQueries({ queryKey: ["projects", project, "shared-secrets"] });
      toast.success(`${r.name} moved to project secrets`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : `Couldn't move ${r.name}`);
    }
  };

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

  const settingsHref = (source: "project" | "instance") =>
    source === "instance" ? "/settings/instance-secrets" : `/projects/${encodeURIComponent(project)}/settings`;

  const masking = (key: string, value: string | undefined, canReveal: boolean) => {
    if (masked || !canReveal || value === undefined) return <span className="text-[var(--text-tertiary)]">••••••••</span>;
    if (!isVisible(key)) return <span className="text-[var(--text-tertiary)]">••••••••</span>;
    return <span className="break-all text-[var(--text-primary)]">{value === "" ? "(empty)" : value}</span>;
  };

  const eye = (key: string, name: string, canReveal: boolean) =>
    !masked && canReveal ? (
      <button
        type="button"
        aria-label={isVisible(key) ? `Hide value of ${name}` : `Show value of ${name}`}
        onClick={() => toggleVisible(key)}
        className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
      >
        {isVisible(key) ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
      </button>
    ) : (
      <span className="h-7 w-7" />
    );

  const rowShell = "grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-x-2 gap-y-0.5 px-2 py-1 sm:grid-cols-[minmax(0,15rem)_minmax(0,1fr)_auto_auto]";
  const nameCell = (name: string, badges: ReactNode) => (
    <div className="flex min-w-0 items-center gap-1.5">
      <span className="truncate font-mono text-[12px] text-[var(--text-primary)]" title={name}>
        {name}
      </span>
      {badges}
    </div>
  );
  const valueCell = (content: ReactNode, onClick?: () => void) => (
    <div
      onClick={onClick}
      className={cn(
        "col-span-3 row-start-2 min-w-0 truncate font-mono text-[12px] sm:col-span-1 sm:col-start-2 sm:row-start-1",
        onClick && "cursor-text",
      )}
    >
      {content}
    </div>
  );

  const renderEditRow = (r: Row, i: number) => (
    <div key={r.id} className="space-y-1 border-l-2 border-[var(--accent)] bg-[var(--bg-secondary)]/60 px-2 py-2">
      <div className="flex flex-col gap-1.5 sm:grid sm:grid-cols-[minmax(0,15rem)_minmax(0,1fr)_auto_auto_auto] sm:items-center">
        <Input
          autoFocus={!r.name}
          placeholder="KEY"
          aria-label={r.name ? `Name of variable ${r.name}` : `Name of variable ${i + 1}`}
          value={r.name}
          onChange={(e) => update(i, { name: e.target.value })}
          className={cn("h-8 font-mono text-[12px]", reservedEnvWarning(r.name) && "border-amber-500/60")}
          disabled={r.fromSecret}
          spellCheck={false}
        />
        <Input
          autoFocus={!!r.name}
          placeholder={r.fromSecret ? "secret reference (pick a new one with 🔗)" : "value or ${{ ref }}"}
          aria-label={r.name ? `Value of ${r.name}` : `Value of variable ${i + 1}`}
          value={r.value}
          onChange={(e) => update(i, { value: e.target.value })}
          className="h-8 min-w-0 font-mono text-[12px]"
          disabled={r.fromSecret}
          spellCheck={false}
        />
        <div className="flex items-center justify-end gap-0.5 sm:contents">
          <ReferencePicker
            project={project}
            excludeService={service}
            onPick={(ref) =>
              update(i, { value: ref, visible: true, fromSecret: false, secretBacked: false, origValueFrom: undefined })
            }
            forceOpen={pickerOpenForIndex === i}
            onForceCloseConsumed={() => setPickerOpenForIndex(null)}
          />
          {!r.addon ? (
            <button
              type="button"
              aria-label={r.name ? `Remove ${r.name}` : "Remove variable"}
              onClick={() => remove(i)}
              className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
            >
              <Trash2 className="h-3.5 w-3.5" />
            </button>
          ) : (
            <span className="h-8 w-8" />
          )}
          <button
            type="button"
            aria-label="Done editing"
            onClick={() => stopEdit(r.id)}
            disabled={!r.name.trim()}
            className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-30"
          >
            <Check className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
      {reservedEnvWarning(r.name) && (
        <p className="font-mono text-[10px] text-amber-400">{reservedEnvWarning(r.name)}</p>
      )}
    </div>
  );

  const renderItem = (it: Item) => {
    if (it.kind === "var") {
      const r = it.row;
      if (canWrite && editing.has(r.id)) return renderEditRow(r, it.index);
      const isRef = r.value.startsWith("${{");
      const key = `var:${r.id}`;
      const plaintext = isRef ? r.resolved : r.value;
      const badges = (
        <>
          {r.addon && <VarBadge title={`From the ${r.addon} addon`}>{r.addon}</VarBadge>}
          {buildTimePrefix(r.name) && (
            <VarBadge title="Inlined into the browser bundle at build time. A change needs a rebuild.">build</VarBadge>
          )}
          {sharedSourceOf.has(r.name) && subscribed.includes(r.name) && (
            <VarBadge tone="warn" title="This service value wins over the project secret of the same name.">
              overrides {sharedSourceOf.get(r.name)}
            </VarBadge>
          )}
          {overrideNames.has(r.name) && (
            <VarBadge tone="warn" title={`${envScope} uses its own value; see the ${envScope} row.`}>
              overridden
            </VarBadge>
          )}
        </>
      );
      const value = isRef && !isVisible(key) ? (
        <span className="inline-flex items-center gap-1 text-[var(--text-secondary)]">
          <Link2 className="h-3 w-3" />
          {r.value.replace(/^\$\{\{\s*|\s*\}\}$/g, "")}
        </span>
      ) : (
        masking(key, plaintext, !r.fromSecret || !!r.value || !!r.resolved)
      );
      return (
        <div key={r.id} className={cn(rowShell, isChanged(r) && "border-l-2 border-[var(--accent)]")}>
          {nameCell(r.name, badges)}
          {/* A masked cell must not open the editor: the input shows plaintext. */}
          {valueCell(value, canWrite && (isRef || isVisible(key)) ? () => startEdit(r.id) : undefined)}
          {eye(key, r.name, plaintext !== undefined && plaintext !== "" || !isRef)}
          <RowMenu
            label={`Actions for ${r.name}`}
            items={[
              { label: "Edit", onSelect: () => startEdit(r.id), disabled: !canWrite },
              { label: "Copy value", onSelect: () => copy(plaintext ?? ""), disabled: masked || !plaintext },
              {
                label: "Move to project secrets",
                onSelect: () => void moveToShared(r),
                disabled: !canWrite || isRef || !!r.addon || r.fromSecret || !r.value || !r.origName,
              },
              { label: "Delete", onSelect: () => remove(it.index), destructive: true, disabled: !canWrite || !!r.addon },
            ]}
          />
        </div>
      );
    }
    if (it.kind === "shared") {
      const key = `shared:${it.name}`;
      const v = sharedValues?.[it.name];
      return (
        <div key={key} className={rowShell}>
          {nameCell(
            it.name,
            <VarBadge tone="accent" title={`Shared ${it.source} secret. Edit it in ${it.source} settings.`}>
              {it.source}
            </VarBadge>,
          )}
          {valueCell(masking(key, v, v !== undefined))}
          {eye(key, it.name, v !== undefined)}
          <RowMenu
            label={`Actions for ${it.name}`}
            items={[
              { label: "Copy value", onSelect: () => copy(v ?? ""), disabled: masked || v === undefined },
              { label: `Edit in ${it.source} settings`, href: settingsHref(it.source) },
              {
                label: "Remove from this service",
                onSelect: () => setPendingUnsub(it.name),
                destructive: true,
                disabled: !canWrite,
              },
            ]}
          />
        </div>
      );
    }
    const key = `override:${it.name}`;
    return (
      <div key={key} className={rowShell}>
        {nameCell(
          it.name,
          <VarBadge
            tone="warn"
            title={`Only on ${envScope}. Change with: kuso env set ${project} ${service} ${it.name}=… --env ${envScope}`}
          >
            {envScope}
          </VarBadge>,
        )}
        {valueCell(masking(key, it.value, it.value !== "" || !it.ref))}
        {eye(key, it.name, it.value !== "" || !it.ref)}
        <RowMenu
          label={`Actions for ${it.name}`}
          items={[{ label: "Copy value", onSelect: () => copy(it.value), disabled: masked || !it.value }]}
        />
      </div>
    );
  };

  // Walk the sorted items, collapsing each prefix family into one header row
  // unless it's expanded or holds a row being edited.
  const body: ReactNode[] = [];
  const doneGroups = new Set<string>();
  for (const it of shown) {
    const g = groupOf.get(it.name);
    if (!g) {
      body.push(renderItem(it));
      continue;
    }
    if (doneGroups.has(g)) continue;
    doneGroups.add(g);
    const members = shown.filter((m) => groupOf.get(m.name) === g);
    const forcedOpen = members.some((m) => m.kind === "var" && (editing.has(m.row.id) || isChanged(m.row)));
    const open = forcedOpen || openGroups.has(g);
    const sources = Array.from(
      new Set(members.map((m) => (m.kind === "var" ? m.row.addon : m.kind === "shared" ? m.source : envScope)).filter(Boolean)),
    );
    body.push(
      <button
        key={`group:${g}`}
        type="button"
        onClick={() =>
          !forcedOpen &&
          setOpenGroups((prev) => {
            const next = new Set(prev);
            if (next.has(g)) next.delete(g);
            else next.add(g);
            return next;
          })
        }
        className="flex w-full items-center gap-1.5 px-2 py-1.5 text-left hover:bg-[var(--bg-secondary)]/60"
      >
        <ChevronRight
          className={cn("h-3 w-3 text-[var(--text-tertiary)] transition-transform", open && "rotate-90")}
        />
        <span className="font-mono text-[12px] text-[var(--text-primary)]">{g}_*</span>
        <span className="font-mono text-[11px] text-[var(--text-tertiary)]">{members.length}</span>
        {sources.map((s) => (
          <VarBadge key={s}>{s}</VarBadge>
        ))}
      </button>,
    );
    if (open) {
      body.push(
        <div key={`group-body:${g}`} className="divide-y divide-[var(--border-subtle)] bg-[var(--bg-secondary)]/40">
          {members.map(renderItem)}
        </div>,
      );
    }
  }

  const empty = blankRows.length === 0 && items.length === 0;

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="font-heading text-sm font-semibold tracking-tight text-[var(--text-primary)]">Variables</h3>
        {envScope !== "production" && (
          <VarBadge
            tone="warn"
            title={`Service variables apply to every environment. Values only for ${envScope} show a ${envScope} badge.`}
          >
            all environments
          </VarBadge>
        )}
        {masked && (
          <span title="Values are hidden. Reading them needs the admin role." className="text-[var(--text-tertiary)]">
            <Lock className="h-3.5 w-3.5" />
          </span>
        )}
        <DeployStatus drift={drift.data} />
        <div className="ml-auto flex items-center gap-1.5">
          <div className="relative">
            <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[var(--text-tertiary)]" />
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search"
              aria-label="Search variables"
              className="h-8 w-36 pl-7 text-[12px]"
            />
          </div>
          {!masked && (
            <button
              type="button"
              aria-label={revealAll ? "Hide all values" : "Show all values"}
              title={revealAll ? "Hide all values" : "Show all values"}
              onClick={() => {
                setRevealAll((v) => !v);
                setShownValues(new Set());
              }}
              className="inline-flex h-8 w-8 items-center justify-center rounded-md border border-[var(--border-subtle)] text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
            >
              {revealAll ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
            </button>
          )}
          {canWrite && (
            <AddMenu
              onNew={() => addRow()}
              onShared={() => setSharedOpen(true)}
              onPaste={() => setPasteOpen(true)}
              onGithub={onAddGithubSignIn}
            />
          )}
        </div>
      </div>

      {empty ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-8 text-center text-xs text-[var(--text-tertiary)]">
          No variables yet.
        </p>
      ) : (
        <div className="divide-y divide-[var(--border-subtle)] rounded-md border border-[var(--border-subtle)]">
          {blankRows.map(({ row, index }) => renderEditRow(row, index))}
          {body}
          {shown.length === 0 && q && (
            <p className="px-3 py-4 text-center text-xs text-[var(--text-tertiary)]">No match.</p>
          )}
        </div>
      )}

      {missing.length > 0 && !q && (
        <div className="rounded-md border border-dashed border-[var(--border-subtle)]">
          {missing.map((m) => (
            <div key={m.name} className="flex items-center gap-2 px-2 py-1.5">
              <span className="truncate font-mono text-[12px] text-[var(--text-tertiary)]">{m.name}</span>
              {m.crash ? (
                <VarBadge tone="warn" title={m.crash}>
                  crashed without it
                </VarBadge>
              ) : (
                <VarBadge title="Referenced in your code but not set">missing</VarBadge>
              )}
              {canWrite && (
                <button
                  type="button"
                  onClick={() => addRow(m.name)}
                  className="ml-auto rounded px-2 py-0.5 text-[11px] text-[var(--accent)] hover:bg-[var(--bg-tertiary)]"
                >
                  Add
                </button>
              )}
            </div>
          ))}
        </div>
      )}

      <SharedPickerDialog
        open={sharedOpen}
        onOpenChange={setSharedOpen}
        project={project}
        shape={sharedSub.data}
        saving={subscriptionSaving}
        onApply={(keys) =>
          void setSubscription(keys).then(
            () => setSharedOpen(false),
            () => undefined,
          )
        }
      />
      <PasteEnvDialog open={pasteOpen} onOpenChange={setPasteOpen} onApply={applyPaste} />
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
      <ConfirmDialog
        open={pendingUnsub != null}
        title={`Remove ${pendingUnsub ?? ""} from this service?`}
        body="The service stops receiving this shared secret. The running pod keeps it until its next restart or deploy."
        confirmLabel="Remove"
        pending={subscriptionSaving}
        onCancel={() => setPendingUnsub(null)}
        onConfirm={() => {
          const name = pendingUnsub;
          if (!name) return;
          // setSubscription already toasts its own error.
          setSubscription(subscribed.filter((k) => k !== name)).then(
            () => {
              toast.success(`Removed ${name} from ${service}`);
              setPendingUnsub(null);
            },
            () => setPendingUnsub(null),
          );
        }}
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
      if (e.key !== "Escape") return;
      // Capture + claim: close just this menu, not the service overlay.
      claimEscape(e);
      onClose();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
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

