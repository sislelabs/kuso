"use client";

import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ReconnectingWS, type WSStatus } from "@/lib/ws-client";
import { ApiError } from "@/lib/api-client";
import { getProfile } from "@/features/auth/api";
import { sessionQueryKey } from "@/features/auth";

export interface LogFrame {
  type: "log" | "ping" | "phase" | "error" | "notice";
  pod?: string;
  stream?: string;
  line?: string;
  ts?: string;
  value?: string;
  message?: string;
}

export interface LogLine {
  // Monotonic per-stream id: a stable React key that survives the
  // MAX_LINES slice (index keys shift every row once the cap kicks in).
  id: number;
  pod: string;
  line: string;
  ts: string;
  stream?: string;
}

export interface UseLogStreamResult {
  lines: LogLine[];
  phase: string | null;
  status: WSStatus;
  error: string | null;
  notice: string | null;
  clear: () => void;
}

const MAX_LINES = 10_000;

// createReplayFilter drops lines a reconnect replays. Every new socket
// asks for the same ?tail=N, so after a drop the server re-sends up to
// N lines per pod we already have. Kube log timestamps are per-pod
// monotonic, so a line older than the newest one seen for its pod is a
// replay; a line at exactly that instant is a replay only if its text
// was already seen at that instant. Lines without a timestamp pass.
export function createReplayFilter(): (l: { pod: string; ts?: string; line: string }) => boolean {
  const last = new Map<string, { ms: number; seen: Set<string> }>();
  return (l) => {
    if (!l.ts) return true;
    const ms = Date.parse(l.ts);
    if (Number.isNaN(ms)) return true;
    // Sub-millisecond part of RFC3339Nano so same-ms lines still order.
    const frac = /\.(\d+)/.exec(l.ts)?.[1] ?? "";
    const key = frac.padEnd(9, "0") + "\u0000" + l.line;
    const cur = last.get(l.pod);
    if (!cur || ms > cur.ms) {
      last.set(l.pod, { ms, seen: new Set([key]) });
      return true;
    }
    if (ms < cur.ms) return false;
    if (cur.seen.has(key)) return false;
    cur.seen.add(key);
    return true;
  };
}

export function useLogStream(
  project: string,
  service: string,
  env = "production",
  tail = 200
): UseLogStreamResult {
  const [lines, setLines] = useState<LogLine[]>([]);
  const [phase, setPhase] = useState<string | null>(null);
  const [status, setStatus] = useState<WSStatus>("connecting");
  const [error, setError] = useState<string | null>(null);
  // notice is an informational end-of-stream message (e.g. an expired
  // build log), shown in place of the log body rather than as an error.
  const [notice, setNotice] = useState<string | null>(null);
  const wsRef = useRef<ReconnectingWS<LogFrame> | null>(null);

  // Track end-of-stream so the close handler can distinguish a
  // healthy "build finished, server hung up" from a genuine drop.
  // Build streams legitimately end (kaniko exits, archive ships,
  // server sends phase=completed + Close frame); without this the
  // UI would always flash "connection lost" right after a successful
  // build. We use a ref instead of state because the WS callbacks
  // close over the initial render and wouldn't see state updates.
  const completedRef = useRef(false);
  const qc = useQueryClient();

  useEffect(() => {
    if (!project || !service) return;
    setLines([]);
    setError(null);
    setNotice(null);
    setStatus("connecting");
    completedRef.current = false;

    const accept = createReplayFilter();
    let nextId = 0;
    // Frames arrive in bursts (a 200-line backfill, a chatty build).
    // Buffer them and commit every ~50ms instead of one state update +
    // full-array copy per line.
    let pending: LogLine[] = [];
    let timer: ReturnType<typeof setTimeout> | null = null;
    const flush = () => {
      timer = null;
      if (pending.length === 0) return;
      const batch = pending;
      pending = [];
      setLines((prev) => {
        const next = prev.length + batch.length > MAX_LINES
          ? prev.concat(batch).slice(-MAX_LINES)
          : prev.concat(batch);
        return next;
      });
    };

    const path = `/ws/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/logs?env=${encodeURIComponent(env)}&tail=${tail}`;
    const ws = new ReconnectingWS<LogFrame>({
      path,
      onStatus: (s, info) => {
        setStatus(s);
        if (s === "open") {
          setError(null);
          return;
        }
        // Suppress error chrome once the server has signalled
        // end-of-stream — a 1000 close after phase=completed is the
        // expected exit, and even a 1006 (no Close frame) is fine if
        // we already saw the completed phase.
        if (completedRef.current) return;
        if (info?.gaveUp) {
          setError("can't connect to the log stream — reload to retry");
          return;
        }
        if (s === "error") setError("connection error");
        if (s === "closed" && info?.code === 1006) setError("connection lost");
      },
      // A refused upgrade looks like any other drop. Ask the API whether
      // the session is still good: if not, stop retrying and let the
      // session query bounce the user to /login.
      onHandshakeFailure: async () => {
        try {
          await getProfile();
          return "retry";
        } catch (e) {
          if (e instanceof ApiError && e.status === 401) {
            void qc.invalidateQueries({ queryKey: sessionQueryKey });
            return "stop";
          }
          return "retry";
        }
      },
      onFrame: (f) => {
        if (f.type === "log") {
          const l = {
            pod: f.pod ?? "",
            line: f.line ?? "",
            ts: f.ts,
            stream: f.stream,
          };
          if (!accept(l)) return;
          pending.push({ ...l, id: nextId++, ts: l.ts ?? new Date().toISOString() });
          if (timer === null) timer = setTimeout(flush, 50);
        } else if (f.type === "phase" && f.value) {
          setPhase(f.value);
          // Terminal phases the build poller emits at end-of-stream.
          // Any future close on this WS is expected, not a drop.
          const v = f.value.toLowerCase();
          if (v === "completed" || v === "succeeded" || v === "failed" || v === "cancelled") {
            completedRef.current = true;
          }
        } else if (f.type === "error") {
          setError(f.message ?? "stream error");
        } else if (f.type === "notice") {
          completedRef.current = true;
          setNotice(f.message ?? "");
        }
        // ping is ignored — its purpose is keep-alive
      },
    });
    wsRef.current = ws;
    ws.open();

    return () => {
      ws.close();
      if (timer !== null) clearTimeout(timer);
      wsRef.current = null;
    };
  }, [project, service, env, tail, qc]);

  return {
    lines,
    phase,
    status,
    error,
    notice,
    clear: () => setLines([]),
  };
}
