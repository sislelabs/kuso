"use client";

import { useEffect, useRef, useState } from "react";
import { ReconnectingWS, type WSStatus } from "@/lib/ws-client";

export interface LogFrame {
  type: "log" | "ping" | "phase" | "error";
  pod?: string;
  stream?: string;
  line?: string;
  ts?: string;
  value?: string;
  message?: string;
}

export interface LogLine {
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
  clear: () => void;
}

const MAX_LINES = 10_000;

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
  const wsRef = useRef<ReconnectingWS<LogFrame> | null>(null);

  useEffect(() => {
    if (!project || !service) return;
    setLines([]);
    setError(null);
    setStatus("connecting");

    const path = `/ws/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/logs?env=${encodeURIComponent(env)}&tail=${tail}`;
    const ws = new ReconnectingWS<LogFrame>({
      path,
      onStatus: (s, info) => {
        setStatus(s);
        if (s === "error") setError("connection error");
        if (s === "closed" && info?.code === 1006) setError("connection lost");
      },
      onFrame: (f) => {
        if (f.type === "log") {
          setLines((prev) => {
            const next = [...prev, {
              pod: f.pod ?? "",
              line: f.line ?? "",
              ts: f.ts ?? new Date().toISOString(),
              stream: f.stream,
            }];
            if (next.length > MAX_LINES) return next.slice(-MAX_LINES);
            return next;
          });
        } else if (f.type === "phase" && f.value) {
          setPhase(f.value);
        } else if (f.type === "error") {
          setError(f.message ?? "stream error");
        }
        // ping is ignored — its purpose is keep-alive
      },
    });
    wsRef.current = ws;
    ws.open();

    return () => {
      ws.close();
      wsRef.current = null;
    };
  }, [project, service, env, tail]);

  return {
    lines,
    phase,
    status,
    error,
    clear: () => setLines([]),
  };
}
