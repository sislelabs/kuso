import { useSyncExternalStore } from "react";
import type { StagedEnv, StagedHealthcheckTest, StagedProject, StagedService, StagedSettings, StagedState } from "./types";

// A module-level store, so staged edits outlive the panel, the overlay
// and the full-page shell, and every view of a service reads the same
// set. Mirrored to sessionStorage (this tab only) so a reload keeps them.

const STORAGE_KEY = "kuso.staged.v1";
// Write-only credential: never persisted, even per tab.
const VOLATILE_FIELDS = new Set(["repoToken"]);
const EMPTY: StagedState = {};

let state: StagedState = EMPTY;
let hydrated = false;
const listeners = new Set<() => void>();

function hydrate() {
  if (hydrated || typeof window === "undefined") return;
  hydrated = true;
  try {
    const raw = window.sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return;
    const parsed: unknown = JSON.parse(raw);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) state = parsed as StagedState;
  } catch {
    state = EMPTY;
  }
}

// persistable drops what must not touch storage: secret-looking variable
// values and write-only fields. They stay staged in memory for this visit.
export function persistable(s: StagedState): StagedState {
  const out: StagedState = {};
  for (const [project, services] of Object.entries(s)) {
    const p: StagedProject = {};
    for (const [service, svc] of Object.entries(services)) {
      const next: StagedService = {};
      if (svc.env) {
        const env: StagedEnv = {
          set: svc.env.set.filter((e) => !e.sensitive),
          unset: svc.env.unset.map((u) => (u.sensitive ? { ...u, before: undefined } : u)),
        };
        if (env.set.length || env.unset.length) next.env = env;
      }
      if (svc.settings) {
        const fields = Object.fromEntries(Object.entries(svc.settings.fields).filter(([k]) => !VOLATILE_FIELDS.has(k)));
        if (Object.keys(fields).length) next.settings = { fields, base: svc.settings.base };
      }
      if (svc.healthcheckTest) next.healthcheckTest = svc.healthcheckTest;
      if (next.env || next.settings) p[service] = next;
    }
    if (Object.keys(p).length) out[project] = p;
  }
  return out;
}

function persist() {
  if (typeof window === "undefined") return;
  try {
    const data = persistable(state);
    if (Object.keys(data).length === 0) window.sessionStorage.removeItem(STORAGE_KEY);
    else window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(data));
  } catch {
    // Private mode / quota: staging still works for this page load.
  }
}

function isEmptyService(s: StagedService): boolean {
  const env = s.env && (s.env.set.length > 0 || s.env.unset.length > 0);
  const settings = s.settings && Object.keys(s.settings.fields).length > 0;
  return !env && !settings;
}

function commit(project: string, service: string, fn: (prev: StagedService) => StagedService) {
  hydrate();
  const proj = state[project] ?? {};
  const next = fn(proj[service] ?? {});
  const nextProj: StagedProject = { ...proj };
  if (isEmptyService(next)) delete nextProj[service];
  else nextProj[service] = next;
  const nextState: StagedState = { ...state };
  if (Object.keys(nextProj).length === 0) delete nextState[project];
  else nextState[project] = nextProj;
  state = nextState;
  persist();
  for (const l of listeners) l();
}

function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function getStagedState(): StagedState {
  hydrate();
  return state;
}

function withoutKey(errors: Record<string, string> | undefined, match: (k: string) => boolean) {
  if (!errors) return undefined;
  const next = Object.fromEntries(Object.entries(errors).filter(([k]) => !match(k)));
  return Object.keys(next).length ? next : undefined;
}

export function stageEnv(project: string, service: string, env: StagedEnv | undefined) {
  commit(project, service, (prev) => ({
    ...prev,
    env: env && (env.set.length || env.unset.length) ? env : undefined,
    errors: withoutKey(prev.errors, (k) => k.startsWith("env:")),
  }));
}

export function stageSettings(project: string, service: string, settings: StagedSettings | undefined) {
  commit(project, service, (prev) => ({
    ...prev,
    settings: settings && Object.keys(settings.fields).length ? settings : undefined,
    errors: withoutKey(prev.errors, (k) => k === "settings"),
  }));
}

// Records the last health check test. A service with nothing staged
// keeps no record (the test was of the deployed path).
export function recordHealthcheckTest(project: string, service: string, test: StagedHealthcheckTest | undefined) {
  hydrate();
  if (!state[project]?.[service] && test) return;
  commit(project, service, (prev) => ({ ...prev, healthcheckTest: test }));
}

export function setStagedErrors(project: string, service: string, errors: Record<string, string> | undefined) {
  commit(project, service, (prev) => ({ ...prev, errors: errors && Object.keys(errors).length ? errors : undefined }));
}

// Drops the given env names and/or the settings after they applied.
export function clearApplied(project: string, service: string, applied: { env: string[]; settings: boolean }) {
  const names = new Set(applied.env);
  commit(project, service, (prev) => ({
    ...prev,
    env: prev.env && {
      set: prev.env.set.filter((e) => !names.has(e.name)),
      unset: prev.env.unset.filter((u) => !names.has(u.name)),
    },
    settings: applied.settings ? undefined : prev.settings,
    healthcheckTest: applied.settings ? undefined : prev.healthcheckTest,
  }));
}

export function discardService(project: string, service: string) {
  commit(project, service, () => ({}));
}

export function discardProject(project: string) {
  hydrate();
  if (!state[project]) return;
  const next = { ...state };
  delete next[project];
  state = next;
  persist();
  for (const l of listeners) l();
}

// hasUnpersistedStaged: some of the project's staged edits exist only in
// memory (secret values, write-only fields) and a reload would drop them.
export function hasUnpersistedStaged(project: string): boolean {
  hydrate();
  const live = state[project];
  if (!live) return false;
  const kept = persistable({ [project]: live })[project] ?? {};
  return Object.entries(live).some(([service, svc]) => countServiceChanges(svc) !== countServiceChanges(kept[service]));
}

export function countServiceChanges(s: StagedService | undefined): number {
  if (!s) return 0;
  return (s.env?.set.length ?? 0) + (s.env?.unset.length ?? 0) + Object.keys(s.settings?.fields ?? {}).length;
}

export function useStagedProject(project: string): StagedProject | undefined {
  return useSyncExternalStore(
    subscribe,
    () => getStagedState()[project],
    () => undefined,
  );
}

export function useStagedService(project: string, service: string): StagedService | undefined {
  return useSyncExternalStore(
    subscribe,
    () => getStagedState()[project]?.[service],
    () => undefined,
  );
}

// Test hook: reset module state between cases.
export function __resetStagedForTests() {
  state = EMPTY;
  hydrated = false;
}
