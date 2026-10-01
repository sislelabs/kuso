// Phases the updater writes once it has stopped working on a rollout.
// rolled-back / rollback-failed come from the operator health gate.
const TERMINAL_PHASES = new Set(["", "done", "failed", "rolled-back", "rollback-failed"]);

export function isUpdateInFlight(phase: string | undefined): boolean {
  return !TERMINAL_PHASES.has(phase ?? "");
}
