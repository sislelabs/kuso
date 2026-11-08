"use client";

import { useEffect, useRef, useState } from "react";
import {
  inspectRepo,
  useDetectRuntime,
  useScanAddons,
  type AddonSuggestion,
  type DetectRuntimeResponse,
} from "@/features/github";
import { friendlyApiError } from "@/features/projects/names";
import type { PickedRepo } from "./RepoPicker";

export type DetectState = "idle" | "detecting" | "done" | "failed";

// The runtime used when detection finds nothing or can't read the repo:
// nixpacks inspects the source at build time and covers far more stacks
// than the detector's marker files.
export const FALLBACK_RUNTIME = "nixpacks";

export interface RepoDetection {
  state: DetectState;
  result: DetectRuntimeResponse | null;
  // Why detection failed (server note or request error).
  note: string | null;
  suggestions: AddonSuggestion[];
}

// useRepoDetection detects runtime + port for the picked repo, and again
// when the monorepo path changes (debounced so typing "apps/api" doesn't
// fire one request per keystroke). onDetected receives the runtime and
// port to apply; on failure it gets the fallback runtime and no port.
export function useRepoDetection(
  picked: PickedRepo | null,
  path: string,
  onDetected: (runtime: string, port: number | null) => void,
): RepoDetection {
  const detect = useDetectRuntime();
  const scan = useScanAddons();
  const [det, setDet] = useState<RepoDetection>({ state: "idle", result: null, note: null, suggestions: [] });
  const apply = useRef(onDetected);
  apply.current = onDetected;
  // Latest-wins token: detect requests aren't cancellable, so a slow
  // response for an earlier pick must not stamp a later pick's form.
  const seq = useRef(0);

  useEffect(() => {
    const mine = ++seq.current;
    if (!picked) {
      setDet({ state: "idle", result: null, note: null, suggestions: [] });
      return;
    }
    setDet((d) => ({ ...d, state: "detecting", note: null }));
    const done = (result: DetectRuntimeResponse) => {
      if (mine !== seq.current) return;
      setDet((d) => ({ ...d, state: "done", result, note: null }));
      apply.current(result.runtime, result.port || null);
    };
    const failed = (note: string | null) => {
      if (mine !== seq.current) return;
      setDet((d) => ({ ...d, state: "failed", result: null, note }));
      apply.current(FALLBACK_RUNTIME, null);
    };
    const trimmed = path.trim();
    const timer = setTimeout(
      () => {
        if (picked.byUrl) {
          // No App: the server reads the repo by URL. Addon scanning is
          // an App-only feature, so there are no suggestions here.
          setDet((d) => ({ ...d, suggestions: [] }));
          inspectRepo({
            url: picked.byUrl.url,
            token: picked.byUrl.token || undefined,
            branch: picked.repo.defaultBranch,
            path: trimmed || undefined,
          })
            .then((res) => (res.runtime ? done(res.runtime) : failed(res.runtimeNote ?? null)))
            .catch((e) => failed(friendlyApiError(e, "the repository couldn't be read")));
          return;
        }
        const [owner, repo] = picked.repo.fullName.split("/");
        const target = {
          installationId: picked.installationId,
          owner: owner ?? "",
          repo: repo ?? "",
          branch: picked.repo.defaultBranch,
          path: trimmed,
        };
        detect
          .mutateAsync(target)
          .then(done)
          .catch((e) => failed(friendlyApiError(e, "the repository couldn't be read")));
        scan
          .mutateAsync(target)
          .then((res) => {
            if (mine === seq.current) setDet((d) => ({ ...d, suggestions: res.suggestions ?? [] }));
          })
          .catch(() => {
            if (mine === seq.current) setDet((d) => ({ ...d, suggestions: [] }));
          });
      },
      trimmed ? 500 : 0,
    );
    return () => clearTimeout(timer);
    // detect/scan are stable mutation objects for this component.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [picked, path]);

  return det;
}

// The detector's rules, in order — see DetectFromFiles in
// server-go/internal/github/repo_tree.go.
export const DETECTION_RULES: readonly { files: string; runtime: string }[] = [
  { files: "Dockerfile", runtime: "dockerfile" },
  { files: "index.html with no app manifest", runtime: "static" },
  { files: "*.csproj", runtime: "buildpacks" },
  { files: "package.json, go.mod, Cargo.toml, requirements.txt or pyproject.toml", runtime: "nixpacks" },
];
