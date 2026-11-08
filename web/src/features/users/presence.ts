import { relativeTime } from "@/lib/format";

// The server bumps lastLogin on sign-in and, throttled to once per 5 min,
// on any authenticated request, so "within 5 min" means currently active.
export const ACTIVE_WINDOW_MS = 5 * 60_000;

// An OAuth account is created by its first sign-in, so a missing
// lastLogin there only means the server didn't record it (servers before
// last-seen tracking never stamped OAuth sign-ins).
function isOAuth(provider: string | undefined): boolean {
  return !!provider && provider !== "local";
}

export function lastSeenLabel(
  lastLogin: string | null | undefined,
  now: number = Date.now(),
  provider?: string,
): string {
  const never = isOAuth(provider) ? "Last seen unknown" : "Never signed in";
  if (!lastLogin) return never;
  const t = new Date(lastLogin).getTime();
  if (Number.isNaN(t)) return never;
  if (now - t < ACTIVE_WINDOW_MS) return "Active now";
  return `Last seen ${relativeTime(lastLogin, now)}`;
}

// A user who has never signed in (admin-created, not yet used).
export function hasNeverSignedIn(user: { lastLogin?: string | null; provider?: string }): boolean {
  return !user.lastLogin && !isOAuth(user.provider);
}
