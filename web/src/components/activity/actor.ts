// Audit actors that aren't a person: the server writes "system" for
// agent/remediation entries and "kuso" for its own jobs; older or
// background entries leave the actor empty.
const SYSTEM_ACTORS = new Set(["", "system", "kuso"]);

export function isSystemActor(id: string | undefined): boolean {
  const v = (id ?? "").trim().toLowerCase();
  return SYSTEM_ACTORS.has(v) || v.startsWith("system:");
}

// Display label for an actor the user directory can't resolve: the
// system name as-is, a long id shortened.
export function actorLabel(id: string | undefined): string {
  const v = (id ?? "").trim();
  if (!v) return "system";
  return v.length > 12 ? `${v.slice(0, 8)}…` : v;
}

// Two-letter initials for the avatar fallback.
export function actorInitials(name: string): string {
  const letters = name.replace(/[^\p{L}\p{N}]/gu, "");
  return (letters || "?").slice(0, 2).toUpperCase();
}

// Enter and Space toggle an expandable row, like a native button.
export function isToggleKey(key: string): boolean {
  return key === "Enter" || key === " " || key === "Spacebar";
}
