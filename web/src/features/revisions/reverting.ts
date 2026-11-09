import { useEffect, useSyncExternalStore } from "react";

// A revert of a failing rollout takes a few seconds before the env stops
// reading "rollout_failing". This remembers, per service, that a revert
// was sent so every surface can say "Reverting…" in between instead of
// still offering the revert.
const started = new Map<string, number>();
const listeners = new Set<() => void>();
const MAX_MS = 3 * 60_000;

const key = (project: string, service: string) => `${project}/${service}`;
const emit = () => listeners.forEach((l) => l());

export function markReverting(project: string, service: string): void {
  started.set(key(project, service), Date.now());
  emit();
}

export function clearReverting(project: string, service: string): void {
  if (started.delete(key(project, service))) emit();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

// useReverting is true from a sent revert until the rollout stops failing
// (or a few minutes pass, in case the revert itself never lands).
export function useReverting(project: string, service: string, failing: boolean): boolean {
  const at = useSyncExternalStore(
    subscribe,
    () => started.get(key(project, service)),
    () => undefined,
  );
  useEffect(() => {
    if (at !== undefined && !failing) clearReverting(project, service);
  }, [at, failing, project, service]);
  return at !== undefined && failing && Date.now() - at < MAX_MS;
}
