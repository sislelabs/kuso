"use client";

// ServiceTerminalPanel — an interactive xterm.js shell into one of a
// service's pods, backed by the kuso server's terminal WebSocket
// (GET /ws/projects/{p}/services/{s}/terminal). The server proxies a
// `kubectl exec`-equivalent stream so the browser needs no cluster
// credentials.
//
// Wire protocol (see server-go terminal_ws.go):
//   client→server: plain-text frame = stdin; {"resize":{cols,rows}} = TTY resize
//   server→client: binary frame = raw stdout/stderr
//
// We use a raw WebSocket (not the ReconnectingWS wrapper): a shell
// session is stateful — silently reconnecting would drop the user
// into a fresh shell mid-command, which is worse than a clear
// "session ended" message.

import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { RefreshCw, TerminalSquare } from "lucide-react";
import { Button } from "@/components/ui/button";

type Status = "connecting" | "open" | "closed";

export function ServiceTerminalPanel({
  project,
  service,
  env = "production",
}: {
  project: string;
  service: string;
  env?: string;
}) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const [status, setStatus] = useState<Status>("connecting");
  // sessionKey is bumped by "New session" to tear down + remount.
  const [sessionKey, setSessionKey] = useState(0);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;

    setStatus("connecting");
    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily:
        "ui-monospace, SFMono-Regular, Menlo, Consolas, 'Liberation Mono', monospace",
      theme: {
        background: "#0b0e14",
        foreground: "#c9d1d9",
        cursor: "#c9d1d9",
      },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host);
    fit.fit();

    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const url =
      `${proto}//${window.location.host}` +
      `/ws/projects/${encodeURIComponent(project)}` +
      `/services/${encodeURIComponent(service)}/terminal` +
      `?env=${encodeURIComponent(env)}`;
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";

    const sendResize = () => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ resize: { cols: term.cols, rows: term.rows } }));
      }
    };

    ws.onopen = () => {
      setStatus("open");
      // Push the initial terminal size so the pod's shell wraps
      // correctly from the first prompt.
      sendResize();
      term.focus();
    };
    ws.onmessage = (e) => {
      if (typeof e.data === "string") {
        term.write(e.data);
      } else {
        term.write(new Uint8Array(e.data as ArrayBuffer));
      }
    };
    ws.onclose = (e) => {
      setStatus("closed");
      if (e.code !== 1000) {
        term.write(
          `\r\n\x1b[33m[kuso] connection closed${e.reason ? ": " + e.reason : ""}\x1b[0m\r\n`,
        );
      }
    };
    ws.onerror = () => setStatus("closed");

    // Keystrokes → stdin.
    const dataDisp = term.onData((d) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(d);
    });
    // Terminal resize → fit + notify the pod's TTY.
    const resizeDisp = term.onResize(() => sendResize());

    const onWindowResize = () => {
      try {
        fit.fit();
      } catch {
        // host may be detached mid-teardown — ignore
      }
    };
    window.addEventListener("resize", onWindowResize);
    // ResizeObserver catches the overlay panel growing/shrinking even
    // when the window itself doesn't change.
    const ro = new ResizeObserver(() => onWindowResize());
    ro.observe(host);

    return () => {
      window.removeEventListener("resize", onWindowResize);
      ro.disconnect();
      dataDisp.dispose();
      resizeDisp.dispose();
      ws.close();
      term.dispose();
    };
  }, [project, service, env, sessionKey]);

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-xs text-[var(--text-secondary)]">
          <TerminalSquare className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
          <span className="font-mono">
            sh · {service} · {env}
          </span>
          <span
            className={
              "ml-1 inline-flex items-center gap-1 font-mono text-[10px] " +
              (status === "open"
                ? "text-emerald-400"
                : status === "connecting"
                  ? "text-amber-400"
                  : "text-[var(--text-tertiary)]")
            }
          >
            ● {status}
          </span>
        </div>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => setSessionKey((k) => k + 1)}
          title="Start a fresh shell session"
        >
          <RefreshCw className="h-3.5 w-3.5" /> New session
        </Button>
      </div>
      <div
        ref={hostRef}
        className="h-[420px] w-full overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[#0b0e14] p-2"
      />
      <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
        Interactive shell in the service&apos;s pod. Requires the deployer role.
        The session ends when you leave this tab.
      </p>
    </div>
  );
}
