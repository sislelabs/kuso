"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api-client";
import { ReconnectingWS } from "@/lib/ws-client";
import { buildsQueryKey } from "@/features/services/hooks";

export interface BuildLogLine {
  id: number;
  line: string;
}

// The REST tail caps at 2000 lines server-side; build logs rarely exceed it
// and the archive the poller keeps is shorter still.
const STORED_LINES = 2000;
const LIVE_MAX_LINES = 10_000;

interface TailResponse {
  lines: { pod: string; line: string }[] | null;
}

export async function fetchBuildLog(project: string, service: string, buildId: string): Promise<BuildLogLine[]> {
  const res = await api<TailResponse>(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/logs?env=${encodeURIComponent(`build:${buildId}`)}&lines=${STORED_LINES}`,
  );
  return (res.lines ?? []).map((l, i) => ({ id: i, line: l.line }));
}

// useStoredBuildLog reads a finished build's log over REST. A finished build
// has nothing left to stream, so a WebSocket here only produced a socket that
// closed immediately and showed up as "connection lost".
export function useStoredBuildLog(project: string, service: string, buildId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["projects", project, "services", service, "builds", buildId, "log"] as const,
    queryFn: () => fetchBuildLog(project, service, buildId),
    enabled: enabled && !!project && !!service && !!buildId,
    staleTime: 5 * 60_000,
    retry: (count, err) => !(err instanceof ApiError && err.status === 404) && count < 2,
    meta: { skipGlobalErrorToast: true },
  });
}

export type LiveLogState = "connecting" | "live" | "reconnecting" | "ended" | "offline";

interface LiveFrame {
  type: "log" | "ping" | "phase" | "error" | "notice";
  line?: string;
  value?: string;
  message?: string;
}

const TERMINAL_PHASES = new Set(["completed", "succeeded", "failed", "cancelled"]);

// useLiveBuildLog follows a running build's pod over the logs WebSocket.
// Drops reconnect with backoff (state "reconnecting"); after the handshake
// budget runs out the state is "offline" and reconnect() starts over.
export function useLiveBuildLog(project: string, service: string, buildId: string, enabled: boolean) {
  const [lines, setLines] = useState<BuildLogLine[]>([]);
  const [state, setState] = useState<LiveLogState>("connecting");
  const [message, setMessage] = useState<string | null>(null);
  const [nonce, setNonce] = useState(0);
  const endedRef = useRef(false);
  const qc = useQueryClient();

  useEffect(() => {
    if (!enabled || !project || !service || !buildId) return;
    endedRef.current = false;
    setLines([]);
    setMessage(null);
    setState("connecting");

    let nextId = 0;
    let pending: BuildLogLine[] = [];
    let timer: ReturnType<typeof setTimeout> | null = null;
    const flush = () => {
      timer = null;
      if (pending.length === 0) return;
      const batch = pending;
      pending = [];
      setLines((prev) => {
        const next = prev.concat(batch);
        return next.length > LIVE_MAX_LINES ? next.slice(-LIVE_MAX_LINES) : next;
      });
    };

    const path = `/ws/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/logs?env=${encodeURIComponent(`build:${buildId}`)}&tail=1000`;
    const ws = new ReconnectingWS<LiveFrame>({
      path,
      maxHandshakeFailures: 6,
      onStatus: (s, info) => {
        if (endedRef.current) return;
        if (s === "open") {
          // Every (re)connect replays the pod's tail from the start, and
          // build lines carry no timestamps to dedupe on, so start clean.
          pending = [];
          setLines([]);
          nextId = 0;
          setState("live");
          return;
        }
        if (info?.gaveUp) {
          setState("offline");
          return;
        }
        if (s === "closed" || s === "error") setState("reconnecting");
      },
      onFrame: (f) => {
        if (f.type === "log") {
          pending.push({ id: nextId++, line: f.line ?? "" });
          if (timer === null) timer = setTimeout(flush, 50);
        } else if (f.type === "phase" && f.value && TERMINAL_PHASES.has(f.value.toLowerCase())) {
          endedRef.current = true;
          flush();
          setState("ended");
          void qc.invalidateQueries({ queryKey: buildsQueryKey(project, service) });
        } else if (f.type === "notice") {
          endedRef.current = true;
          setMessage(f.message ?? null);
          setState("ended");
        } else if (f.type === "error") {
          setMessage(f.message ?? null);
        }
      },
    });
    ws.open();
    return () => {
      ws.close();
      if (timer !== null) clearTimeout(timer);
    };
  }, [project, service, buildId, enabled, nonce, qc]);

  const reconnect = useCallback(() => setNonce((n) => n + 1), []);
  return { lines, state, message, reconnect };
}

const ERROR_RE =
  /\b(error|err!|fatal|panic|failed|failure|exception|traceback|cannot|could not|not found|denied|exit code|exited with|oomkilled|killed)\b|^\s*>>>|\s>>>\s|^Dockerfile:\d+/i;
const NOISE_RE = /\bnpm (notice|warn)\b|\bdeprecated\b|^\s*#\d+ (sha256|resolve|transferring|load)\b/i;

// extractFailureLines picks the lines worth reading first in a failed build.
// buildkit prints a self-contained summary after the failing step (the
// "------" block with the step, its last output and the Dockerfile excerpt),
// so when it exists that block is the answer. Otherwise fall back to
// error-looking lines, each with one line of context before it.
export function extractFailureLines(lines: string[], max = 60): string[] {
  let lastError = findLastIndex(lines, (l) => /^#\d+ ERROR:/.test(l.trim()));
  if (lastError < 0) lastError = findLastIndex(lines, (l) => /^error: failed to solve/i.test(l.trim()));
  if (lastError >= 0) {
    const blockStart = lines.findIndex((l, i) => i > lastError && l.trim() === "------");
    const block = lines
      .slice(blockStart >= 0 ? blockStart : lastError + 1)
      .filter((l) => l.trim() !== "" && !NOISE_RE.test(l));
    return [lines[lastError].trim(), ...block.slice(-(max - 1))];
  }
  const picked = new Set<number>();
  lines.forEach((l, i) => {
    if (!ERROR_RE.test(l) || NOISE_RE.test(l)) return;
    if (i > 0 && lines[i - 1].trim() !== "") picked.add(i - 1);
    picked.add(i);
  });
  return [...picked].sort((a, b) => a - b).map((i) => lines[i]).slice(-max);
}

function findLastIndex<T>(arr: T[], pred: (v: T) => boolean): number {
  for (let i = arr.length - 1; i >= 0; i--) if (pred(arr[i])) return i;
  return -1;
}
