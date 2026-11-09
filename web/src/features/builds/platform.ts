// Platform failures: builds that stalled or failed because kuso couldn't
// run the build pod (builder image, scheduling, pod config), as opposed
// to something in the user's repo. The server stamps them with
// failureClass.kind "build_platform" and a message starting with
// PLATFORM_FAILURE_PREFIX; archived builds keep only the message.

import type { DeployStall } from "./types";

export const PLATFORM_FAILURE_KIND = "build_platform";
export const PLATFORM_FAILURE_PREFIX = "kuso couldn't start the build";

export function isPlatformFailure(b: { errorMessage?: string; failureClass?: { kind?: string } }): boolean {
  return b.failureClass?.kind === PLATFORM_FAILURE_KIND || (b.errorMessage ?? "").startsWith(PLATFORM_FAILURE_PREFIX);
}

// stallHeadline is the one line a stalled in-flight build shows. Every
// stall is kuso's side, so the builder-image and pod-config ones say so.
export function stallHeadline(stall: Pick<DeployStall, "kind" | "summary">): string {
  switch (stall.kind) {
    case "image_pull":
      return "kuso can't start the build: builder image unavailable";
    case "config_error":
      return `kuso can't start the build: ${stall.summary.replace(/^The build pod can't start: /, "")}`;
    default:
      return stall.summary;
  }
}

// stallNote says whose problem a stall is and when kuso gives up on it.
export function stallNote(stall: Pick<DeployStall, "failsAt">, now: number = Date.now()): string {
  const base = "A problem on kuso's side, not in your code.";
  const at = stall.failsAt ? Date.parse(stall.failsAt) : NaN;
  if (!Number.isFinite(at)) return base;
  const mins = Math.ceil((at - now) / 60_000);
  if (mins <= 0) return `${base} kuso is stopping the build.`;
  return `${base} If it doesn't recover, kuso stops the build in ${mins} min.`;
}
