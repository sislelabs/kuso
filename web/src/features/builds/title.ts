import { buildRefLabel } from "./live";
import type { DeployBuild } from "./types";

const lower = (s: string | undefined) => (s ?? "").toLowerCase();

// commitSubject is the first line of the commit message, trimmed.
export function commitSubject(b: { commitMessage?: string }): string {
  return (b.commitMessage ?? "").split("\n")[0].trim();
}

// buildTitle names a build by what it built: the commit subject, else the
// ref and branch. How it was triggered is a separate chip (buildTrigger).
export function buildTitle(b: DeployBuild): string {
  const subject = commitSubject(b);
  if (subject) return subject;
  const ref = buildRefLabel(b, 7);
  if (b.commitSha && b.branch) return `Commit ${ref} on ${b.branch}`;
  if (b.commitSha) return `Commit ${ref}`;
  return b.branch ? `Build of ${b.branch}` : ref || "Build";
}

export type BuildTriggerKind = "push" | "manual" | "retry" | "rebuild" | "api" | "first" | "rollback";

export interface BuildTrigger {
  kind: BuildTriggerKind;
  label: string;
  // Longer explanation for the chip's tooltip.
  hint: string;
}

// buildTrigger says how a build started, as lowercase note text ("via
// push", "manual", …). A manual build of a commit whose previous attempt
// failed is a retry (a rebuild once it succeeded); of a commit that already
// built fine, a rebuild. `builds` is the service's history, newest first.
export function buildTrigger(b: DeployBuild, builds: DeployBuild[]): BuildTrigger | undefined {
  const by = b.triggeredByUser;
  switch (b.triggeredBy) {
    case "webhook":
      return { kind: "push", label: "via push", hint: by ? `Pushed by ${by}` : "Started by a git push" };
    case "api":
      return { kind: "api", label: "via API", hint: "Started through the API or CLI" };
    case "system":
      return { kind: "first", label: "first deploy", hint: "Started when the service was created" };
    case "user": {
      const who = by ? ` by ${by}` : "";
      const earlier = sameCommitBefore(b, builds);
      if (earlier && ["failed", "release-failed", "cancelled"].includes(lower(earlier.status))) {
        const hint = `Retried${who} after the previous attempt ${lower(earlier.status).replace("-", " ")}`;
        // "retry" on a green row reads like an action; it was a rebuild that worked.
        return lower(b.status) === "succeeded"
          ? { kind: "rebuild", label: "rebuild", hint }
          : { kind: "retry", label: "retry", hint };
      }
      if (earlier) return { kind: "rebuild", label: "rebuild", hint: `Same commit built again${who}` };
      return { kind: "manual", label: "manual", hint: `Deployed${who} from the dashboard` };
    }
    default:
      return undefined;
  }
}

function sameCommitBefore(b: DeployBuild, builds: DeployBuild[]): DeployBuild | undefined {
  if (!b.commitSha) return undefined;
  const idx = builds.indexOf(b);
  if (idx < 0) return undefined;
  return builds.slice(idx + 1).find((o) => o.commitSha === b.commitSha && (o.branch ?? "") === (b.branch ?? ""));
}
