"use client";

import { smoothScroll } from "@/lib/reduced-motion";
import { useEffect, useMemo, useRef, useState, type ClipboardEvent, type ReactNode } from "react";
import Link from "next/link";
import { useOverlayDirty } from "@/components/service/ServiceOverlay";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import {
  Check,
  ChevronRight,
  ClipboardPaste,
  Copy,
  FileDown,
  Eye,
  EyeOff,
  Link2,
  Lock,
  AlertTriangle,
  Plus,
  Search,
  Trash2,
  Variable,
  X,
} from "lucide-react";
import { danglingRef } from "./envvars/dangling";
import { useService, useServiceEnv, useServiceEnvOverrides, useDetectedEnv, useDrift } from "@/features/services";
import { unsetServiceEnvVar } from "@/features/services/api";
import { stageEnv, useStagedService, type StagedEnv } from "@/features/staged";
import { StagedChangesBar } from "@/components/staged/StagedChangesBar";
import { StagedChip } from "@/components/staged/StagedChip";
import { setSharedSecret } from "@/features/project-secrets/api";
import { applySubscriptionDelta, putSubscriptionRebased } from "@/features/addons/subscriptionDelta";
import { useProject, useAddons } from "@/features/projects";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCanOnProject, Perms } from "@/features/auth";
import { api, ApiError } from "@/lib/api-client";
import {
  ENV_MASK_SENTINEL,
  ENV_NAME_RE,
  addonShortByConnSecret,
  buildTimePrefix,
  prefixGroups,
  reservedEnvWarning,
  rid,
  rowsShallowEqual,
  rowUnchanged,
  toRow,
  type Row,
} from "@/components/service/envVarTransforms";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { cn } from "@/lib/utils";
import {
  AddMenu,
  DeployStatus,
  INSTANCE_SHARED_SECRET,
  PasteEnvDialog,
  RowMenu,
  SecretLock,
  SharedPickerDialog,
  VarBadge,
  type SubscribableShape,
} from "@/components/service/EnvVarParts";
import { isSensitiveVar } from "@/components/service/envvars/sensitivity";
import {
  HIDDEN_MARKER,
  parseRawEnv,
  reconcileRaw,
  rowsToRaw,
  type RawIssue,
} from "@/components/service/envvars/rawEnv";
import { applyStagedEnv, sameStagedEnv, toStagedEnv } from "@/components/service/envvars/staging";
import { RawEnvEditor, type RawEnvEditorHandle } from "@/components/service/envvars/RawEnvEditor";
import { EnvExampleDialog } from "@/components/service/envvars/EnvExampleDialog";
import { AddReferenceDialog, RefValueInput, type RefValueInputHandle } from "@/components/service/envvars/refCatalog";
import { restartNote, type RestartImpact } from "@/components/service/envvars/restartImpact";
import { addonPageHref } from "@/components/service/workspace/hrefs";

// Row + the pure transform helpers live in ./envVarTransforms; raw-mode
// parsing, diffing and the sensitivity heuristic live in ./envvars. This
// file keeps only the stateful editor.

// PendingSave is the staged plan for the rows: valueWrites are creates and
// changes (the server picks storage, auto:true), deletes are removed rows
// of any form. Unchanged opaque secret-ref rows are in neither. Edits are
// staged (features/staged) and deploy from the StagedChangesBar together
// with settings edits, so the pods roll once.
interface PendingSave {
  valueWrites: { name: string; value: string }[];
  deletes: string[];
}

type ViewMode = "table" | "raw";
const VIEW_PREF_KEY = "kuso.envvars.view";

function readViewPref(): ViewMode {
  try {
    return window.localStorage.getItem(VIEW_PREF_KEY) === "raw" ? "raw" : "table";
  } catch {
    return "table";
  }
}

function writeViewPref(v: ViewMode) {
  try {
    window.localStorage.setItem(VIEW_PREF_KEY, v);
  } catch {
    // Private mode / blocked storage: the toggle still works for this visit.
  }
}

// addonEmptyNote explains an addon key that arrives empty. Pooler keys
// are the common case: they stay blank while the addon's pooler is off.
function addonEmptyNote(addon: string, key: string): string {
  return /POOL|BOUNCER/i.test(key) ? `${addon} doesn't provide ${key}: its pooler is off` : `${addon} doesn't provide ${key}`;
}

export function EnvVarsEditor({
  project,
  service,
  env: envScope,
  onAddGithubSignIn,
  restartImpact,
  highlight,
}: {
  project: string;
  service: string;
  // env-group scope the parent overlay is showing. Used to fetch
  // per-env Secret keys so the missing-vars list doesn't flag keys that
  // are mounted via the per-env Secret, and to label env overrides.
  env: string;
  onAddGithubSignIn?: () => void;
  // Ready pods a save will restart. Undefined while unknown.
  restartImpact?: RestartImpact;
  // Variable name to scroll to + flash once loaded (?highlight=<KEY>).
  highlight?: string | null;
}) {
  const qc = useQueryClient();
  // Always the reveal read: the server resolves every value to plaintext for
  // callers with secrets:read and masks it for everyone else, so rows load
  // with their real values and the eye only toggles masking.
  const env = useServiceEnv(project, service, true);
  const overrides = useServiceEnvOverrides(project, service, envScope);
  const detected = useDetectedEnv(project, service);

  const drift = useDrift(project, service);
  const addons = useAddons(project);
  const perEnvSecrets = useQuery<{ keys: string[]; env: string | null }>({
    queryKey: ["projects", project, "services", service, "secrets", envScope],
    queryFn: () =>
      api<{ keys: string[]; env: string | null }>(
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/secrets?env=${encodeURIComponent(envScope)}`,
      ).catch((e: unknown) =>
        e instanceof ApiError && e.status === 403 ? { keys: [], env: envScope } : Promise.reject(e),
      ),
    staleTime: 15_000,
  });
  // Shared-env subscription: drives both the shared rows and the missing
  // list (a subscribed key counts as set).
  const sharedSub = useQuery<SubscribableShape>({
    queryKey: ["projects", project, "services", service, "shared-env-keys"],
    queryFn: () =>
      api<SubscribableShape>(
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/shared-env-keys?reveal=true`,
      ).catch((e: unknown) =>
        e instanceof ApiError && e.status === 403 ? { subscribed: [], sources: [] } : Promise.reject(e),
      ),
    staleTime: 30_000,
  });
  const [subscriptionSaving, setSubscriptionSaving] = useState(false);
  // Replaces the service's shared-secret subscription. Applied immediately,
  // not through the save bar: it's its own spec field with its own rollout.
  const [pendingUnsub, setPendingUnsub] = useState<string | null>(null);
  const setSubscription = async (wanted: string[]) => {
    setSubscriptionSaving(true);
    try {
      // The PUT replaces the whole list. Apply only this tab's delta to a
      // fresh read so a key another user subscribed meanwhile isn't dropped.
      const base = sharedSub.data?.subscribed ?? [];
      const path = `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/shared-env-keys`;
      await putSubscriptionRebased(
        async () => (await api<SubscribableShape>(path)).subscribed ?? [],
        (fresh) => applySubscriptionDelta(fresh, base, wanted),
        async (keys, expected) => {
          await api<unknown>(path, { method: "PUT", body: { keys, expected } });
        },
      );
      await qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "shared-env-keys"] });
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "drift"] });
    } catch (e) {
      toastError(e, "Couldn't update project secrets");
      throw e;
    } finally {
      setSubscriptionSaving(false);
    }
  };
  const addonByConn = useMemo(() => addonShortByConnSecret(addons.data ?? [], project), [addons.data, project]);
  const addonNames = useMemo(() => new Set(addonByConn.values()), [addonByConn]);
  // Known env scopes so literalToRef can strip the env-scope segment a
  // resolved service URL carries back to the short service name. Without
  // this the read→save round-trip corrupts ${{ svc.URL }}.
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
    return Array.from(scopes).sort((a, b) => b.length - a.length);
  }, [proj.data]);
  // The server MASKS env values for non-admins ("••••••••" + masked:true).
  // A masked session saving would write the sentinel over real values, so
  // masked ⇒ read-only.
  const masked = env.data?.masked ?? false;
  const canWriteEnv = useCanOnProject(project, Perms.SecretsWrite);
  const canWrite = canWriteEnv && !masked;
  // Reading the example file is an editor action server-side (it may hold
  // real values); image services have no repo to read it from.
  const svcSpec = useService(project, service).data?.spec;
  const canImportExample = canWriteEnv && !!svcSpec && svcSpec.runtime !== "image";
  const [rows, setRows] = useState<Row[]>([]);
  const [dirty, setDirty] = useState(false);
  const discardRef = useRef<() => void>(() => {});
  // Why the current edits can't be staged (bad name, duplicate, malformed
  // .env line); null when everything typed is staged.
  const [stageError, setStageError] = useState<string | null>(null);
  const stagedEnv = useStagedService(project, service)?.env;
  // The staged value this editor last wrote, to tell its own writes from
  // a discard/deploy made elsewhere (the bar, the review, another view).
  const lastStagedRef = useRef<StagedEnv | undefined>(undefined);
  // Staged edits survive closing the panel; only input that couldn't be
  // staged is lost, so that's all the leave guard warns about.
  useOverlayDirty("variables", dirty && !!stageError && canWrite, {
    onDiscard: () => discardRef.current(),
  });
  // Last server-known row set: the save diff and the concurrent-edit
  // detector compare against this, not the local (possibly-edited) rows.
  const baselineFromRows = useRef<Row[]>([]);
  const [editing, setEditing] = useState<Set<string>>(new Set());
  // Values the user unmasked one by one; revealAll unmasks every secret.
  const [shownValues, setShownValues] = useState<Set<string>>(new Set());
  const [revealAll, setRevealAll] = useState(false);
  const [openGroups, setOpenGroups] = useState<Set<string>>(new Set());
  const [search, setSearch] = useState("");
  const [sharedOpen, setSharedOpen] = useState(false);
  const [pasteOpen, setPasteOpen] = useState(false);
  const [refOpen, setRefOpen] = useState(false);
  const [exampleOpen, setExampleOpen] = useState(false);
  const [view, setViewState] = useState<ViewMode>(readViewPref);
  const [rawText, setRawText] = useState("");
  const [rawErrors, setRawErrors] = useState<RawIssue[]>([]);
  const rawRef = useRef<RawEnvEditorHandle>(null);
  const [flashKey, setFlashKey] = useState<string | null>(null);
  const valueInputs = useRef(new Map<string, RefValueInputHandle>());

  // Concurrent-edit guard: when env.data refetches, only re-baseline the
  // rows when the user has nothing dirty; otherwise keep local edits and
  // warn once.
  const [conflictNotified, setConflictNotified] = useState(false);
  useEffect(() => {
    if (!env.data) return;
    const incoming = (env.data.envVars ?? [])
      .map((v) => toRow(v, project, addonByConn, knownScopes))
      .sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
    if (!dirty) {
      if (!rowsShallowEqual(incoming, baselineFromRows.current)) setEditing(new Set());
      setRows(incoming);
      baselineFromRows.current = incoming;
      setConflictNotified(false);
      return;
    }
    const byName = new Map(incoming.map((r) => [r.name, r]));
    const ourDeployLanded =
      !stagedEnv ||
      (stagedEnv.set.every((e) => byName.get(e.name)?.value === e.value) &&
        stagedEnv.unset.every((u) => !byName.has(u.name)));
    if (!conflictNotified && !ourDeployLanded && !rowsShallowEqual(incoming, baselineFromRows.current)) {
      toast("Another edit landed on this service. Deploying your staged changes overwrites it; discard to load it.");
      setConflictNotified(true);
    }
    baselineFromRows.current = incoming;
  }, [env.data, addonByConn, project, dirty, conflictNotified, knownScopes, stagedEnv]);

  // Load staged edits into the rows: on first load, and whenever they
  // change from outside this editor (discarded or deployed elsewhere).
  useEffect(() => {
    if (!env.data || stagedEnv === lastStagedRef.current) return;
    lastStagedRef.current = stagedEnv;
    setRows(applyStagedEnv(baselineFromRows.current, stagedEnv));
    setDirty(!!stagedEnv);
    setEditing(new Set());
    setRawErrors([]);
    setStageError(null);
  }, [env.data, stagedEnv]);

  // Raw text follows the rows whenever there's nothing unsaved in it
  // (first entry, after a save, after a remote change).
  useEffect(() => {
    if (view === "raw" && !dirty) {
      setRawText(rowsToRaw(rows, { hideSecrets: !revealAll }));
      setRawErrors([]);
    }
  }, [view, dirty, rows, revealAll]);

  const markDirty = () => setDirty(true);

  const update = (idx: number, patch: Partial<Row>) => {
    setRows((prev) => prev.map((r, i) => (i === idx ? { ...r, ...patch } : r)));
    if (Object.keys(patch).some((k) => k !== "visible" && k !== "id")) markDirty();
  };
  const remove = (idx: number) => {
    setRows((prev) => prev.filter((_, i) => i !== idx));
    markDirty();
  };

  // Validates + plans the rows as per-key writes/deletes, or says why
  // they can't be staged as they are.
  const planRows = (current: Row[]): PendingSave | { error: string } => {
    const baselineByName = new Map<string, Row>();
    for (const b of baselineFromRows.current) {
      const n = b.name.trim();
      if (n) baselineByName.set(n, b);
    }
    const seen = new Set<string>();
    // Lets the rename guard tell a true rename (old name gone) from a row
    // that shares a name with another still-present row.
    const present = new Set(current.map((r) => r.name.trim()).filter(Boolean));
    const valueWrites: { name: string; value: string }[] = [];
    for (const r of current) {
      const name = r.name.trim();
      if (!name) {
        if (r.value.trim()) return { error: "A variable needs a name before it can be staged" };
        continue;
      }
      if (!ENV_NAME_RE.test(name)) {
        return { error: `"${name}" isn't a valid name. Use letters, digits and _ only` };
      }
      if (seen.has(name)) {
        return { error: `${name} is listed twice` };
      }
      seen.add(name);
      // Opaque secret refs can only be kept or removed; removal goes
      // through the deletes channel below.
      if (r.fromSecret) continue;
      // Never write the masked placeholder back over the real value.
      if (r.value === ENV_MASK_SENTINEL) continue;
      // A blank secret-backed row means "untouched, keep stored value",
      // except after a rename: the old name is deleted below and there's
      // no plaintext to carry over, so refuse rather than lose it.
      if (r.value === "") {
        const wasRenamed = r.secretBacked && !!r.origName && r.origName !== name && !present.has(r.origName);
        if (wasRenamed) {
          return { error: `Reveal and re-enter the value for "${name}": renaming a secret needs its value retyped.` };
        }
        if (r.secretBacked) continue;
      }
      // Writing back an unchanged value would re-run storage selection,
      // re-resolve refs, mint addon overrides and record a revision.
      if (rowUnchanged(r, baselineByName)) continue;
      valueWrites.push({ name, value: r.value });
    }
    const deletes: string[] = [];
    for (const b of baselineFromRows.current) {
      const name = b.name.trim();
      if (name && !present.has(name)) deletes.push(name);
    }
    return { valueWrites, deletes };
  };

  // Brings the raw text into rows. Returns the reconciled rows, or null
  // when the text has errors (shown inline, cursor jumps to the first).
  const commitRaw = (): Row[] | null => {
    const res = reconcileRaw(rawText, rows);
    if (res.errors.length) {
      setRawErrors(res.errors);
      requestAnimationFrame(() => rawRef.current?.goToLine(res.errors[0].line));
      return null;
    }
    setRawErrors([]);
    setRows(res.rows);
    return res.rows;
  };

  const discard = () => {
    setRows(baselineFromRows.current);
    setEditing(new Set());
    setRawErrors([]);
    setStageError(null);
    setDirty(false);
    lastStagedRef.current = undefined;
    stageEnv(project, service, undefined);
  };
  discardRef.current = discard;

  const stagedForData = useRef<unknown>(undefined);
  // Stage every valid edit as it's made. Raw mode stages the parsed text
  // without rewriting the rows under the cursor.
  useEffect(() => {
    const baselineMoved = stagedForData.current !== env.data;
    stagedForData.current = env.data;
    if (!dirty || !canWrite || !env.data) return;
    let current = rows;
    if (view === "raw") {
      const res = reconcileRaw(rawText, rows);
      if (res.errors.length) {
        setStageError(`Line ${res.errors[0].line}: ${res.errors[0].message}`);
        return;
      }
      current = res.rows;
    }
    const plan = planRows(current);
    if ("error" in plan) {
      setStageError(plan.error);
      return;
    }
    setStageError(null);
    const next = toStagedEnv(plan, baselineFromRows.current, current);
    // The deployed values caught up with every edit (our deploy landed):
    // settle back to a clean editor instead of holding stale edit rows.
    if (!next && baselineMoved) {
      lastStagedRef.current = undefined;
      stageEnv(project, service, undefined);
      setRows(baselineFromRows.current);
      setEditing(new Set());
      setDirty(false);
      return;
    }
    if (sameStagedEnv(next, lastStagedRef.current)) return;
    lastStagedRef.current = next;
    stageEnv(project, service, next);
  }, [rows, rawText, view, dirty, canWrite, env.data, project, service]);

  // Per-row undo: back to the deployed value, or gone if it's new.
  const revertRow = (r: Row) => {
    const base = baselineFromRows.current.find((b) => b.name === (r.origName ?? r.name).trim());
    setRows((prev) => (base ? prev.map((x) => (x.id === r.id ? base : x)) : prev.filter((x) => x.id !== r.id)));
    stopEdit(r.id);
    markDirty();
  };
  const restoreRemoved = (name: string) => {
    const base = baselineFromRows.current.find((b) => b.name === name);
    if (!base) return;
    setRows((prev) => [...prev, base]);
    markDirty();
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
  // Keyed by name, not row id: a refetch while dirty re-mints baseline ids,
  // which made every row look edited.
  const baselineByName = useMemo(() => {
    const m = new Map<string, Row>();
    for (const b of baselineFromRows.current) if (b.name.trim()) m.set(b.name.trim(), b);
    return m;
    // eslint-disable-next-line react-hooks/exhaustive-deps -- baseline changes with rows/env.data
  }, [rows, env.data]);
  const isChanged = (r: Row) => {
    const b = baselineByName.get((r.origName ?? r.name).trim());
    return !b || b.name !== r.name || b.value !== r.value;
  };
  const nameCounts = useMemo(() => {
    const m = new Map<string, number>();
    for (const r of rows) {
      const n = r.name.trim();
      if (n) m.set(n, (m.get(n) ?? 0) + 1);
    }
    return m;
  }, [rows]);

  type Item =
    | { kind: "var"; name: string; row: Row; index: number }
    | { kind: "shared"; name: string; source: "project" | "instance" }
    | { kind: "override"; name: string; value: string; ref: boolean };

  const items = useMemo<Item[]>(() => {
    const out: Item[] = [];
    rows.forEach((row, index) => {
      if (row.name.trim() && !(editing.has(row.id) && !row.origName))
        out.push({ kind: "var", name: row.name, row, index });
    });
    for (const k of subscribed) {
      if (!rowNames.has(k)) out.push({ kind: "shared", name: k, source: sharedSourceOf.get(k) ?? "project" });
    }
    for (const v of overrideVars) {
      out.push({ kind: "override", name: v.name, value: v.value ?? "", ref: !!v.valueFrom });
    }
    return out.sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
  }, [rows, subscribed, rowNames, sharedSourceOf, overrideVars, editing]);

  // Rows added this session that are still open render on top, so typing
  // a name doesn't make the row jump into its sorted slot mid-edit.
  const newRows = rows
    .map((row, index) => ({ row, index }))
    .filter(({ row }) => !row.name.trim() || (editing.has(row.id) && !row.origName));

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
  const matches = (it: Item) => {
    if (!q) return true;
    if (it.name.toUpperCase().includes(q)) return true;
    // Values are searchable too, but never the masked ones.
    if (it.kind === "var" && !isSensitiveVar(it.row.name, it.row.value)) return it.row.value.toUpperCase().includes(q);
    return false;
  };
  const shown = q ? items.filter(matches) : items;
  const groupOf = useMemo(() => (q ? new Map<string, string>() : prefixGroups(items.map((i) => i.name))), [items, q]);

  // ── Highlight (?highlight=<KEY>) ─────────────────────────────────────
  const handledHighlight = useRef<string | null>(null);
  useEffect(() => {
    if (!highlight || handledHighlight.current === highlight || !env.data) return;
    // The row sync runs in an effect too; wait until it has landed.
    if (rows.length === 0 && (env.data.envVars?.length ?? 0) > 0) return;
    const target =
      items.find((i) => i.name === highlight) ??
      items.find((i) => i.name.toUpperCase() === highlight.toUpperCase());
    const ghost = missing.find((m) => m.name.toUpperCase() === highlight.toUpperCase());
    if (!target && !ghost) {
      if (!sharedSub.isPending && !overrides.isPending) {
        handledHighlight.current = highlight;
        toast(`${highlight} isn't set on ${service}.`);
      }
      return;
    }
    handledHighlight.current = highlight;
    const name = target?.name ?? ghost?.name ?? highlight;
    if (view === "raw") setView("table");
    setSearch("");
    const g = groupOf.get(name);
    if (g) setOpenGroups((prev) => new Set(prev).add(g));
    setFlashKey(name);
    // Wait a frame for the group to expand and the row to render.
    requestAnimationFrame(() =>
      requestAnimationFrame(() => {
        const el = document.querySelector<HTMLElement>(`[data-env-key="${CSS.escape(name)}"]`);
        el?.scrollIntoView({ block: "center", behavior: smoothScroll() });
      }),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps -- one-shot per highlight value once data is in
  }, [highlight, env.data, rows, items, missing, sharedSub.isPending, overrides.isPending]);

  useEffect(() => {
    if (!flashKey) return;
    const t = window.setTimeout(() => setFlashKey(null), 2600);
    return () => window.clearTimeout(t);
  }, [flashKey]);

  const isVisible = (key: string, sensitive: boolean) => !sensitive || revealAll || shownValues.has(key);
  const toggleVisible = (key: string) =>
    setShownValues((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  const copy = (value: string, name: string) => {
    void navigator.clipboard.writeText(value).then(
      () => toast.success(`Copied ${name}`),
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

  const addRow = (name = "", value = "") => {
    const row: Row = { id: rid(), name, value, fromSecret: false, secretBacked: false, visible: true };
    setRows((prev) => [...prev, row]);
    setEditing((prev) => new Set(prev).add(row.id));
    markDirty();
    return row;
  };

  // Stages parsed KEY=value entries: existing names get the new value,
  // the rest become new rows. `replaceId` is a blank row the paste came
  // from, dropped so it doesn't linger empty.
  const stageEntries = (entries: { name: string; value: string }[], replaceId?: string) => {
    setRows((prev) => {
      const next = prev.filter((r) => r.id !== replaceId);
      for (const p of entries) {
        const i = next.findIndex((r) => r.name.trim() === p.name);
        if (i >= 0) next[i] = { ...next[i], value: p.value, fromSecret: false, origValueFrom: undefined };
        else next.push({ id: rid(), name: p.name, value: p.value, fromSecret: false, secretBacked: false, visible: true });
      }
      return next;
    });
    if (replaceId) stopEdit(replaceId);
    markDirty();
  };

  // Adds .env.example keys as new rows. Names already present are skipped,
  // so an import never overwrites a value.
  const importExample = (entries: { name: string; value: string }[]) => {
    const fresh = entries.filter((e) => !rowNames.has(e.name));
    if (fresh.length === 0) return;
    setRows((prev) => [
      ...prev,
      ...fresh.map((e) => ({ id: rid(), name: e.name, value: e.value, fromSecret: false, secretBacked: false, visible: true })),
    ]);
    markDirty();
    toast(`${fresh.length} ${fresh.length === 1 ? "variable" : "variables"} added from .env.example. They're staged for the next deploy.`);
  };

  const applyPaste = (text: string): string | null => {
    const { entries, errors } = parseRawEnv(text);
    if (errors.length) {
      const first = errors[0];
      return `Line ${first.line}: ${first.message}${errors.length > 1 ? ` (and ${errors.length - 1} more)` : ""}. Nothing was added.`;
    }
    if (entries.length === 0) return "No KEY=value lines found.";
    stageEntries(entries);
    setPasteOpen(false);
    toast(`${entries.length} ${entries.length === 1 ? "variable" : "variables"} staged for the next deploy.`);
    return null;
  };

  // Pasting a multi-line .env into a name field splits it into rows.
  const onNamePaste = (e: ClipboardEvent<HTMLInputElement>, r: Row) => {
    const text = e.clipboardData.getData("text");
    if (!text.includes("=") || (!text.includes("\n") && !/^\s*(export\s+)?[A-Za-z_][A-Za-z0-9_]*\s*=/.test(text))) return;
    const { entries, errors } = parseRawEnv(text);
    if (entries.length === 0) return;
    e.preventDefault();
    stageEntries(entries, r.name.trim() || r.value ? undefined : r.id);
    if (errors.length) {
      toast.error(`Added ${entries.length}; line ${errors[0].line} skipped: ${errors[0].message}`);
    } else if (entries.length > 1) {
      toast(`Split ${entries.length} variables from the paste.`);
    }
  };

  const insertReference = (name: string, ref: string) => {
    const i = rows.findIndex((r) => r.name.trim() === name);
    let id: string;
    if (i >= 0) {
      id = rows[i].id;
      update(i, { value: ref, fromSecret: false, origValueFrom: undefined, resolved: undefined });
    } else {
      const row: Row = { id: rid(), name, value: ref, fromSecret: false, secretBacked: false, visible: true };
      id = row.id;
      setRows((prev) => [...prev, row]);
      markDirty();
    }
    // Open the row and put the caret in its value once the dialog has
    // closed (it skips its own focus return after an insert).
    setEditing((prev) => new Set(prev).add(id));
    setFlashKey(name);
    window.setTimeout(() => valueInputs.current.get(id)?.focus(), 0);
  };

  // Promote a service var to a project secret: write it to <project>-shared,
  // subscribe this service, then drop the service var. The service var wins
  // on the pod until the last step, so there's no window without the value.
  const moveToShared = async (r: Row) => {
    if (dirty) {
      toast.error("Deploy or discard your staged variable changes first");
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
      toastError(e, `Couldn't move ${r.name}`);
    }
  };

  const toggleRevealAll = () => {
    const next = !revealAll;
    if (view === "raw" && dirty) {
      // Re-render the text in the other mode without losing typed edits.
      const res = commitRaw();
      if (!res) return;
      setRawText(rowsToRaw(res, { hideSecrets: !next }));
    }
    setRevealAll(next);
    setShownValues(new Set());
  };

  const setView = (next: ViewMode) => {
    if (next === view) return;
    if (next === "table") {
      // Leaving raw applies the text; malformed lines keep you in raw.
      if (dirty && !commitRaw()) return;
    } else {
      setRawText(rowsToRaw(rows, { hideSecrets: !revealAll }));
      setRawErrors([]);
      setEditing(new Set());
    }
    setViewState(next);
    writeViewPref(next);
  };

  if (env.isPending) {
    return (
      <div className="space-y-2" aria-busy="true" aria-label="Loading variables">
        {Array.from({ length: 5 }, (_, i) => (
          <div key={i} className="h-9 animate-pulse rounded-md bg-[var(--bg-secondary)]" />
        ))}
      </div>
    );
  }
  if (env.isError) {
    return (
      <div className="rounded-md border border-[var(--error)]/40 bg-[var(--error-subtle)] px-3 py-3 text-sm text-[var(--error)]">
        Couldn&apos;t load variables: {env.error?.message}
      </div>
    );
  }

  const settingsHref = (source: "project" | "instance") =>
    source === "instance" ? "/settings/instance-secrets" : `/projects/${encodeURIComponent(project)}/settings`;

  const MASK = <span className="text-[var(--text-tertiary)]">••••••••</span>;
  const valueText = (key: string, value: string | undefined, sensitive: boolean) => {
    if (masked || value === undefined) return MASK;
    if (!isVisible(key, sensitive)) return MASK;
    if (value === "") return <span className="italic text-[var(--text-tertiary)]">empty</span>;
    return <span className="text-[var(--text-primary)]">{value}</span>;
  };

  const iconBtn =
    "inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]";
  const eye = (key: string, name: string, show: boolean) =>
    !masked && show ? (
      <button
        type="button"
        aria-label={shownValues.has(key) || revealAll ? `Hide value of ${name}` : `Show value of ${name}`}
        title={shownValues.has(key) || revealAll ? "Hide value" : "Show value"}
        onClick={() => toggleVisible(key)}
        className={iconBtn}
      >
        {shownValues.has(key) || revealAll ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
      </button>
    ) : (
      <span className="h-7 w-7 shrink-0" />
    );
  const copyBtn = (value: string | undefined, name: string) =>
    !masked && value ? (
      <button
        type="button"
        aria-label={`Copy value of ${name}`}
        title="Copy value"
        onClick={() => copy(value, name)}
        className={cn(iconBtn, "sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100")}
      >
        <Copy className="h-3.5 w-3.5" />
      </button>
    ) : (
      <span className="h-7 w-7 shrink-0" />
    );

  const rowShell = (name: string, extra?: string) =>
    cn(
      "group grid grid-cols-[minmax(0,1fr)_auto_auto_auto] items-center gap-x-1 gap-y-0.5 px-3 py-1.5 transition-colors duration-700 sm:grid-cols-[minmax(0,16rem)_minmax(0,1fr)_auto_auto_auto]",
      flashKey === name && "bg-[var(--warning-subtle)]",
      extra,
    );
  const nameCell = (name: string, badges: ReactNode) => (
    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
      <span className="min-w-0 truncate font-mono text-xs font-medium text-[var(--text-primary)]" title={name}>
        {name}
      </span>
      {badges}
    </div>
  );
  const valueCell = (content: ReactNode, onClick?: () => void) => (
    <div
      onClick={onClick}
      className={cn(
        "col-span-4 row-start-2 min-w-0 truncate font-mono text-xs sm:col-span-1 sm:col-start-2 sm:row-start-1",
        onClick && "cursor-text",
      )}
    >
      {content}
    </div>
  );

  const renderEditRow = (r: Row, i: number) => {
    const name = r.name.trim();
    const nameError = !name
      ? ""
      : !ENV_NAME_RE.test(name)
        ? /^[0-9]/.test(name)
          ? "Names can't start with a digit"
          : "Use letters, digits and _ only"
        : (nameCounts.get(name) ?? 0) > 1
          ? `${name} is already set. Rename one of them`
          : "";
    const reserved = reservedEnvWarning(name);
    return (
      <div
        key={r.id}
        data-env-key={name || undefined}
        className={cn(
          "space-y-1.5 border-l-2 border-[var(--accent)] bg-[var(--bg-secondary)] px-3 py-2.5",
          flashKey === name && "bg-[var(--warning-subtle)]",
        )}
      >
        <div className="flex flex-col gap-1.5 sm:flex-row sm:items-center">
          <Input
            autoFocus={!r.name}
            placeholder="KEY or paste a .env"
            aria-label={r.name ? `Name of variable ${r.name}` : `Name of new variable`}
            aria-invalid={!!nameError}
            value={r.name}
            onChange={(e) => update(i, { name: e.target.value })}
            onPaste={(e) => onNamePaste(e, r)}
            className={cn("h-8 font-mono text-xs sm:w-64 sm:shrink-0", reserved && !nameError && "border-[var(--warning)]/60")}
            disabled={r.fromSecret}
            spellCheck={false}
            autoComplete="off"
          />
          <div className="flex min-w-0 flex-1 items-center gap-1">
            <RefValueInput
              ref={(h) => {
                if (h) valueInputs.current.set(r.id, h);
                else valueInputs.current.delete(r.id);
              }}
              project={project}
              service={service}
              autoFocus={!!r.name}
              placeholder={r.fromSecret ? "Mounted secret. Type a value to replace it" : "value, or ${{ to reference"}
              ariaLabel={r.name ? `Value of ${r.name}` : "Value of new variable"}
              value={r.value}
              onChange={(v) => update(i, { value: v, ...(r.fromSecret ? { fromSecret: false, origValueFrom: undefined } : {}) })}
              onSubmit={() => name && !nameError && stopEdit(r.id)}
              className="h-8 font-mono text-xs"
            />
            <button
              type="button"
              aria-label="Insert a reference"
              title="Insert a ${{ reference }}"
              onClick={() => {
                const v = r.value.includes("${{") ? r.value : r.value + "${{ ";
                update(i, { value: v, fromSecret: false, origValueFrom: undefined });
                requestAnimationFrame(() => valueInputs.current.get(r.id)?.focus());
              }}
              className={cn(iconBtn, "h-8 w-8")}
            >
              <Link2 className="h-3.5 w-3.5" />
            </button>
            {!r.addon ? (
              <button
                type="button"
                aria-label={r.name ? `Remove ${r.name}` : "Remove variable"}
                title="Remove"
                onClick={() => remove(i)}
                className={cn(iconBtn, "h-8 w-8 hover:text-[var(--error)]")}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </button>
            ) : (
              <span className="h-8 w-8 shrink-0" />
            )}
            <button
              type="button"
              aria-label="Done editing"
              title="Done (Enter)"
              onClick={() => stopEdit(r.id)}
              disabled={!name || !!nameError}
              className={cn(iconBtn, "h-8 w-8 disabled:opacity-30")}
            >
              <Check className="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
        {nameError ? (
          <p className="text-xs text-[var(--error)]">{nameError}</p>
        ) : reserved ? (
          <p className="text-xs text-[var(--warning)]">{reserved}</p>
        ) : buildTimePrefix(name) ? (
          <p className="text-xs text-[var(--text-tertiary)]">
            {buildTimePrefix(name)}* is inlined into the browser bundle: changing it needs a rebuild, and it&apos;s
            public.
          </p>
        ) : null}
      </div>
    );
  };

  const renderItem = (it: Item) => {
    if (it.kind === "var") {
      const r = it.row;
      if (canWrite && editing.has(r.id)) return renderEditRow(r, it.index);
      const isRef = r.value.startsWith("${{");
      const key = `var:${r.id}`;
      const plaintext = isRef ? r.resolved : r.value;
      // Refs keep their resolved value hidden by default; the ref text
      // itself is shown since it names the source, not the secret.
      const sensitive = isRef || r.fromSecret || isSensitiveVar(r.name, plaintext ?? "");
      // The rows masked by default for being secret. A ref to a service URL
      // is hidden too but isn't a secret; a managed-secret row with a plain
      // value is shown in clear, so it gets no lock either.
      const secretValued = r.fromSecret || isSensitiveVar(r.name, plaintext ?? "");
      // The addon's conn Secret carries the key but no value (e.g. its
      // pooler is off); the pod gets an empty string.
      const dangling = !masked && !isChanged(r) ? danglingRef(r, addonNames) : null;
      // Addon-injected keys (DATABASE_URL) and explicit refs
      // (${{ db.DIRECT_URL }}) are the same thing to the user: one chip.
      const addonBacked = !!r.addon && !isRef && !!r.secretBacked && !isChanged(r);
      const refText = isRef ? r.value.replace(/^\$\{\{\s*|\s*\}\}$/g, "") : addonBacked ? `${r.addon}.${r.name}` : null;
      const badges = (
        <>
          {secretValued && <SecretLock />}
          {r.addon && !addonBacked && <VarBadge title={`Injected by the ${r.addon} addon`}>{r.addon}</VarBadge>}
          {buildTimePrefix(r.name) && (
            <VarBadge title="Inlined into the browser bundle at build time. A change needs a rebuild.">build</VarBadge>
          )}
          {r.fromSecret && <VarBadge title="Mounted from a Kubernetes secret kuso doesn't manage">secret ref</VarBadge>}
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
          {isChanged(r) && !stageError && <StagedChip />}
        </>
      );
      const value = dangling ? (
        <span className="inline-flex max-w-full flex-wrap items-center gap-x-2 gap-y-1 font-sans">
          <span
            className="inline-flex min-w-0 items-center gap-1 text-[var(--warning)]"
            title={`${dangling.addon} has no value for ${dangling.key}, so the app reads an empty string.`}
          >
            <AlertTriangle aria-hidden className="h-3.5 w-3.5 shrink-0" />
            <span className="min-w-0">{addonEmptyNote(dangling.addon, dangling.key)}</span>
          </span>
          {dangling.pooler && (
            <Link
              href={addonPageHref(project, dangling.addon, { tab: "settings" })}
              className="text-xs font-medium text-[var(--text-primary)] underline underline-offset-2 hover:text-[var(--link)]"
            >
              Enable pooler
            </Link>
          )}
          {dangling.isRef && canWrite && (
            <button
              type="button"
              onClick={() => remove(it.index)}
              className="text-xs font-medium text-[var(--text-primary)] underline underline-offset-2 hover:text-[var(--danger)]"
            >
              Remove
            </button>
          )}
        </span>
      ) : refText && !(shownValues.has(key) || revealAll) ? (
          <RefChip text={refText} title={r.addon ? `From the ${r.addon} addon` : `Reference to ${refText}`} />
        ) : (
          valueText(key, plaintext, sensitive)
        );
      const canReveal = plaintext !== undefined && plaintext !== "";
      return (
        <div
          key={r.id}
          data-env-key={r.name}
          className={rowShell(r.name, isChanged(r) ? "border-l-2 border-[var(--accent)] pl-[10px]" : undefined)}
        >
          {nameCell(r.name, badges)}
          {valueCell(value, canWrite && (isRef || isVisible(key, sensitive)) ? () => startEdit(r.id) : undefined)}
          {copyBtn(isVisible(key, sensitive) ? plaintext : undefined, r.name)}
          {eye(key, r.name, sensitive && canReveal)}
          <RowMenu
            label={`Actions for ${r.name}`}
            items={[
              { label: "Edit", onSelect: () => startEdit(r.id), disabled: !canWrite },
              ...(isChanged(r) ? [{ label: "Discard staged change", onSelect: () => revertRow(r) }] : []),
              { label: "Copy value", onSelect: () => copy(plaintext ?? "", r.name), disabled: masked || !plaintext },
              { label: "Copy name", onSelect: () => copy(r.name, "name") },
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
      const sensitive = isSensitiveVar(it.name, v ?? "");
      return (
        <div key={key} data-env-key={it.name} className={rowShell(it.name)}>
          {nameCell(
            it.name,
            <>
              {sensitive && <SecretLock />}
              <VarBadge tone="accent" title={`Shared ${it.source} secret. Edit it in ${it.source} settings.`}>
                {it.source}
              </VarBadge>
            </>,
          )}
          {valueCell(valueText(key, v, sensitive))}
          {copyBtn(isVisible(key, sensitive) ? v : undefined, it.name)}
          {eye(key, it.name, sensitive && v !== undefined)}
          <RowMenu
            label={`Actions for ${it.name}`}
            items={[
              { label: "Copy value", onSelect: () => copy(v ?? "", it.name), disabled: masked || v === undefined },
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
    const sensitive = it.ref || isSensitiveVar(it.name, it.value);
    return (
      <div key={key} data-env-key={it.name} className={rowShell(it.name)}>
        {nameCell(
          it.name,
          <>
            {sensitive && <SecretLock />}
            <VarBadge
              tone="warn"
              title={`Only on ${envScope}. Change with: kuso env set ${project} ${service} ${it.name}=… --env ${envScope}`}
            >
              {envScope} only
            </VarBadge>
          </>,
        )}
        {valueCell(valueText(key, it.value, sensitive))}
        {copyBtn(isVisible(key, sensitive) ? it.value : undefined, it.name)}
        {eye(key, it.name, sensitive && it.value !== "")}
        <RowMenu
          label={`Actions for ${it.name}`}
          items={[{ label: "Copy value", onSelect: () => copy(it.value, it.name), disabled: masked || !it.value }]}
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
      new Set(
        members.map((m) => (m.kind === "var" ? m.row.addon : m.kind === "shared" ? m.source : envScope)).filter(Boolean),
      ),
    );
    body.push(
      <button
        key={`group:${g}`}
        type="button"
        aria-expanded={open}
        onClick={() =>
          !forcedOpen &&
          setOpenGroups((prev) => {
            const next = new Set(prev);
            if (next.has(g)) next.delete(g);
            else next.add(g);
            return next;
          })
        }
        className="flex w-full items-center gap-1.5 px-3 py-2 text-left hover:bg-[var(--bg-secondary)]"
      >
        <ChevronRight
          className={cn("h-3.5 w-3.5 text-[var(--text-tertiary)] transition-transform", open && "rotate-90")}
        />
        <span className="font-mono text-xs font-medium text-[var(--text-primary)]">{g}_*</span>
        <span className="text-xs text-[var(--text-tertiary)]">{members.length} variables</span>
        {sources.map((s) => (
          <VarBadge key={s}>{s}</VarBadge>
        ))}
      </button>,
    );
    if (open) {
      body.push(
        <div key={`group-body:${g}`} className="divide-y divide-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 pl-3">
          {members.map(renderItem)}
        </div>,
      );
    }
  }

  const total = items.length;
  const empty = newRows.length === 0 && total === 0;
  const removedNames = (() => {
    if (view === "raw") return [];
    const present = new Set(rows.map((r) => r.name.trim()).filter(Boolean));
    return baselineFromRows.current.map((b) => b.name.trim()).filter((n) => n && !present.has(n));
  })();
  const pendingCount = (() => {
    if (view === "raw") return null;
    const present = new Set(rows.map((r) => r.name.trim()).filter(Boolean));
    const removed = baselineFromRows.current.filter((b) => b.name.trim() && !present.has(b.name.trim())).length;
    return rows.filter((r) => r.name.trim() && isChanged(r)).length + removed;
  })();
  const showRawToggle = !masked;
  const hasSecrets = items.some(
    (i) =>
      (i.kind === "var" && (isSensitiveVar(i.row.name, i.row.value) || i.row.value.startsWith("${{"))) ||
      (i.kind === "shared" && isSensitiveVar(i.name, sharedValues?.[i.name] ?? "")) ||
      (i.kind === "override" && (i.ref || isSensitiveVar(i.name, i.value))),
  );

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-2">
        <div className="flex min-w-0 items-center gap-2">
          <h3 className="font-heading text-sm font-semibold tracking-tight text-[var(--text-primary)]">Variables</h3>
          <span className="rounded-full bg-[var(--bg-tertiary)] px-2 py-px text-xs tabular-nums text-[var(--text-secondary)]">
            {q && view === "table" ? `${shown.length} of ${total}` : total}
          </span>
          {envScope !== "production" && (
            <VarBadge
              tone="warn"
              title={`Service variables apply to every environment. Values only for ${envScope} show a "${envScope} only" badge.`}
            >
              all environments
            </VarBadge>
          )}
          {masked && (
            <span
              title="Values are hidden. Reading them needs the secrets:read permission."
              className="inline-flex items-center gap-1 text-xs text-[var(--text-tertiary)]"
            >
              <Lock className="h-3.5 w-3.5" /> read-only
            </span>
          )}
          <DeployStatus drift={drift.data} />
        </div>
        <div className="flex w-full items-center gap-1.5 sm:ml-auto sm:w-auto">
          {showRawToggle && (
            <div
              role="radiogroup"
              aria-label="Editor view"
              className="inline-flex shrink-0 rounded-md border border-[var(--border-subtle)] p-0.5"
            >
              {(["table", "raw"] as const).map((v) => (
                <button
                  key={v}
                  type="button"
                  role="radio"
                  aria-checked={view === v}
                  onClick={() => setView(v)}
                  title={v === "raw" ? "Edit everything as one .env file" : "One row per variable"}
                  className={cn(
                    "rounded px-2.5 py-1 text-xs font-medium transition-colors",
                    view === v
                      ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                      : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
                  )}
                >
                  {v === "table" ? "Table" : "Raw .env"}
                </button>
              ))}
            </div>
          )}
          {view === "table" && (
            <div className="relative min-w-0 flex-1 sm:w-44 sm:flex-none">
              <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[var(--text-tertiary)]" />
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape" && search) {
                    e.preventDefault();
                    setSearch("");
                  }
                }}
                placeholder="Filter"
                aria-label="Filter variables"
                data-primary-search
                className="h-8 pl-7 pr-7 text-xs"
              />
              {search && (
                <button
                  type="button"
                  aria-label="Clear filter"
                  onClick={() => setSearch("")}
                  className="absolute right-1 top-1/2 inline-flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              )}
            </div>
          )}
          {view === "raw" && <div className="flex-1" />}
          {!masked && hasSecrets && (
            <button
              type="button"
              aria-label={revealAll ? "Hide secret values" : "Show secret values"}
              aria-pressed={revealAll}
              title={revealAll ? "Hide secret values" : "Show secret values"}
              onClick={toggleRevealAll}
              className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-[var(--border-subtle)] text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
            >
              {revealAll ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
            </button>
          )}
          {canWrite && view === "table" && (
            <AddMenu
              onNew={() => addRow()}
              onReference={() => setRefOpen(true)}
              onShared={() => setSharedOpen(true)}
              onPaste={() => setPasteOpen(true)}
              onImportExample={canImportExample ? () => setExampleOpen(true) : undefined}
              onGithub={onAddGithubSignIn}
            />
          )}
        </div>
      </div>

      {view === "raw" ? (
        <>
          {canWrite && hasSecrets && (
            <p className="text-xs text-[var(--text-secondary)]">
              {revealAll ? (
                "Secret values are shown in clear."
              ) : (
                <>
                  Secret-looking values read <span className="font-mono">{HIDDEN_MARKER}</span>: leave it to keep the
                  value, or type a new one. Use the eye to edit them in place.
                </>
              )}
            </p>
          )}
          <RawEnvEditor
            ref={rawRef}
            value={rawText}
            readOnly={!canWrite}
            errors={rawErrors}
            onChange={(v) => {
              setRawText(v);
              markDirty();
              // Once errors are on screen, keep them current as the user fixes them.
              if (rawErrors.length) {
                const res = reconcileRaw(v, rows);
                setRawErrors(res.errors);
              }
            }}
          />
        </>
      ) : empty ? (
        <div className="flex flex-col items-center rounded-md border border-dashed border-[var(--border-subtle)] px-4 py-10 text-center">
          <span className="mb-3 inline-flex h-9 w-9 items-center justify-center rounded-full bg-[var(--bg-tertiary)] text-[var(--text-secondary)]">
            <Variable className="h-4 w-4" />
          </span>
          <p className="text-sm font-medium text-[var(--text-primary)]">No variables yet</p>
          <p className="mt-1 max-w-sm text-xs text-[var(--text-secondary)]">
            Add the config your app reads from its environment. Reference an addon to wire up a database without
            copying credentials.
          </p>
          {canWrite && (
            <div className="mt-4 flex flex-wrap justify-center gap-2">
              <Button size="sm" onClick={() => addRow()}>
                <Plus className="h-3.5 w-3.5" /> Add variable
              </Button>
              <Button size="sm" variant="outline" onClick={() => setPasteOpen(true)}>
                <ClipboardPaste className="h-3.5 w-3.5" /> Paste .env
              </Button>
              {canImportExample && (
                <Button size="sm" variant="outline" onClick={() => setExampleOpen(true)}>
                  <FileDown className="h-3.5 w-3.5" /> Import .env.example
                </Button>
              )}
              <Button size="sm" variant="outline" onClick={() => setRefOpen(true)}>
                <Link2 className="h-3.5 w-3.5" /> Add reference
              </Button>
            </div>
          )}
        </div>
      ) : (
        <div className="divide-y divide-[var(--border-subtle)] overflow-hidden rounded-md border border-[var(--border-subtle)]">
          {newRows.map(({ row, index }) => renderEditRow(row, index))}
          {body}
          {shown.length === 0 && q && (
            <div className="px-3 py-6 text-center text-xs text-[var(--text-tertiary)]">
              Nothing matches &ldquo;{search.trim()}&rdquo;.{" "}
              <button type="button" onClick={() => setSearch("")} className="text-[var(--text-primary)] underline">
                Clear filter
              </button>
            </div>
          )}
        </div>
      )}

      {view === "table" && missing.length > 0 && !q && (
        <div className="rounded-md border border-dashed border-[var(--border-subtle)]">
          <p className="border-b border-dashed border-[var(--border-subtle)] px-3 py-1.5 text-xs text-[var(--text-secondary)]">
            Your code reads {missing.length === 1 ? "this variable" : "these variables"}, but {missing.length === 1 ? "it isn't" : "they aren't"} set
          </p>
          {missing.map((m) => (
            <div
              key={m.name}
              data-env-key={m.name}
              className={cn(
                "flex items-center gap-2 px-3 py-1.5 transition-colors duration-700",
                flashKey === m.name && "bg-[var(--warning-subtle)]",
              )}
            >
              <span className="truncate font-mono text-xs text-[var(--text-secondary)]">{m.name}</span>
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
                  className="ml-auto rounded px-2 py-0.5 text-xs font-medium text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)]"
                >
                  Add
                </button>
              )}
            </div>
          ))}
        </div>
      )}

      {dirty && canWrite && (stageError || removedNames.length > 0 || (pendingCount ?? 0) > 0) && (
        <div
          role="status"
          className={cn(
            "rounded-md border px-3 py-2 text-xs",
            stageError
              ? "border-[var(--error)]/35 bg-[var(--error-subtle)] text-[var(--text-primary)]"
              : "border-[var(--accent)]/30 bg-[var(--accent-subtle)] text-[var(--text-primary)]",
          )}
        >
          {stageError ? (
            <p>
              <span className="font-medium">Not staged yet:</span> {stageError}
            </p>
          ) : (
            <p>
              {pendingCount === null
                ? "Your .env edits are staged."
                : `${pendingCount} variable ${pendingCount === 1 ? "change is" : "changes are"} staged.`}{" "}
              They deploy with the service&apos;s other staged changes. {restartNote(restartImpact)}
            </p>
          )}
          {removedNames.length > 0 && (
            <ul className="mt-1.5 flex flex-wrap gap-1.5" aria-label="Staged removals">
              {removedNames.map((n) => (
                <li
                  key={n}
                  className="inline-flex items-center gap-1.5 rounded border border-[var(--border-subtle)] bg-[var(--bg-secondary)] py-0.5 pr-1 pl-2"
                >
                  <span className="font-mono line-through">{n}</span>
                  <button
                    type="button"
                    onClick={() => restoreRemoved(n)}
                    className="rounded px-1 text-xs font-medium text-[var(--text-primary)] underline underline-offset-2 hover:bg-[var(--bg-tertiary)]"
                  >
                    Undo
                  </button>
                </li>
              ))}
            </ul>
          )}
          <div className="mt-1.5">
            <button
              type="button"
              onClick={discard}
              className="text-xs font-medium text-[var(--text-secondary)] underline underline-offset-2 hover:text-[var(--text-primary)]"
            >
              Discard variable changes
            </button>
          </div>
        </div>
      )}
      <StagedChangesBar project={project} placement="sticky" />

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
      <EnvExampleDialog
        open={exampleOpen}
        onOpenChange={setExampleOpen}
        project={project}
        service={service}
        existingNames={rowNames}
        onImport={importExample}
      />
      <AddReferenceDialog
        open={refOpen}
        onOpenChange={setRefOpen}
        project={project}
        service={service}
        existingNames={rowNames}
        onInsert={insertReference}
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

function RefChip({ text, title }: { text: string; title: string }) {
  return (
    <span
      title={title}
      className="inline-flex max-w-full items-center gap-1 rounded border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-1.5 py-px font-mono text-xs text-[var(--text-secondary)]"
    >
      <Link2 aria-hidden className="h-3 w-3 shrink-0" />
      <span className="truncate">{text}</span>
    </span>
  );
}
