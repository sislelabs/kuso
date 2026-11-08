import type { DetectRuntimeResponse, RepoReadFailure } from "@/features/github";
import { FALLBACK_RUNTIME, type DetectState } from "./useRepoDetection";

// Where a prefilled value came from: read out of the repo, or a default
// kuso filled in because detection found nothing (or couldn't read).
export type Provenance = "detected" | "default";

export interface DetectionCopy {
  title: string;
  description: string;
  // null: no marker (still detecting, or the user set the value).
  runtime: Provenance | null;
  port: Provenance | null;
}

interface Input {
  state: DetectState;
  result: DetectRuntimeResponse | null;
  failure: RepoReadFailure | null;
  // The form's current values: a value the user changed after detection
  // is no longer "detected".
  runtime: string;
  port: string;
  // "the repo root" or "apps/api/".
  where: string;
}

const CHECK = "Check these settings";
const DEFAULT_PORT = "8080";
const REVIEW = "Review and deploy";

// detectionCopy words step 2 of the add-service quick path so it never
// claims kuso read the repository when it didn't.
export function detectionCopy({ state, result, failure, runtime, port, where }: Input): DetectionCopy {
  if (state === "detecting" || state === "idle") {
    return { title: REVIEW, description: "kuso is reading the repository…", runtime: null, port: null };
  }
  const runtimeDefault: Provenance | null = runtime === FALLBACK_RUNTIME ? "default" : null;
  const portDefault: Provenance | null = !port.trim() || port.trim() === DEFAULT_PORT ? "default" : null;
  if (state === "failed" || !result) {
    const why = failure?.title ? `: ${failure.title}` : "";
    return {
      title: CHECK,
      description: `Detection didn't succeed${why}. The runtime and port below are defaults; check them before deploying.`,
      runtime: runtimeDefault,
      port: portDefault,
    };
  }
  const found = result.reason !== "fallback";
  const runtimeProv = found && runtime === result.runtime ? "detected" : runtimeDefault;
  const portProv = found && !!result.port && port === String(result.port) ? "detected" : portDefault;
  if (!found) {
    return {
      title: CHECK,
      description: `kuso read the repository but found no Dockerfile or app manifest in ${where}, so the runtime and port below are defaults. Check them before deploying.`,
      runtime: runtimeProv,
      port: portProv,
    };
  }
  if (portProv === "default") {
    return {
      title: REVIEW,
      description: "kuso read the repository and filled in the runtime. No port was declared, so the port is a default. Deploy as is, or edit anything first.",
      runtime: runtimeProv,
      port: portProv,
    };
  }
  return {
    title: REVIEW,
    description: "kuso read the repository and filled these in. Deploy as is, or edit anything first.",
    runtime: runtimeProv,
    port: portProv,
  };
}
