// Reconnecting WebSocket wrapper. Each connection attempt authenticates
// with a one-time ticket in the subprotocol (see wsTicketProtocols).
// Use case: log tail streaming. Caller drives via onFrame/onStatus
// callbacks; the wrapper handles auto-reconnect with capped exponential
// backoff and surfaces transient errors as "disconnected" status.

import { api } from "./api-client";

// wsTicketProtocols mints a one-time ticket for a /ws/ path and returns
// the Sec-WebSocket-Protocol list that presents it. The server refuses a
// browser upgrade that only carries the session cookie: an addon console
// is served from this origin, and its scripts would carry the cookie too.
export async function wsTicketProtocols(path: string): Promise<string[]> {
  const bare = path.split("?")[0];
  const r = await api<{ ticket: string }>("/api/ws/ticket", { method: "POST", body: { path: bare } });
  return ["kuso.ticket", r.ticket];
}


export type WSStatus = "connecting" | "open" | "closed" | "error";

export interface WSOptions<F = unknown> {
  /** Path on the kuso server, e.g. /ws/projects/foo/services/bar/logs?env=production */
  path: string;
  onFrame: (frame: F) => void;
  onStatus?: (status: WSStatus, info?: { code?: number; reason?: string; gaveUp?: boolean }) => void;
  /** Max reconnect attempts before giving up. Default Infinity. */
  maxAttempts?: number;
  /**
   * Called when a socket closes without ever opening. A refused upgrade
   * (401/403 expired session, 429 per-user stream cap, 503) is invisible
   * to the browser — it all arrives as close 1006 — so the caller probes
   * over HTTP and answers "stop" (auth is gone: retrying is pointless)
   * or "retry" (keep backing off).
   */
  onHandshakeFailure?: () => Promise<"retry" | "stop"> | "retry" | "stop";
  /** Consecutive never-opened sockets before giving up. Default 10 (~3.5 min of backoff). */
  maxHandshakeFailures?: number;
  /** Subprotocols for one attempt. Default: a fresh ticket from wsTicketProtocols. */
  protocols?: (path: string) => Promise<string[]> | string[];
}

export class ReconnectingWS<F = unknown> {
  private opts: WSOptions<F>;
  private ws: WebSocket | null = null;
  private attempt = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private closed = false;
  private handshakeFailures = 0;

  constructor(opts: WSOptions<F>) {
    this.opts = opts;
  }

  open() {
    if (this.closed) return;
    this.opts.onStatus?.("connecting");
    const protocols = (this.opts.protocols ?? wsTicketProtocols)(this.opts.path);
    if (Array.isArray(protocols)) {
      this.connect(protocols);
      return;
    }
    protocols.then(
      (p) => this.connect(p),
      // No ticket (session gone, server down) is a refused handshake.
      () => {
        if (!this.closed) void this.handleHandshakeFailure(0, "ticket");
      },
    );
  }

  private connect(protocols: string[]) {
    if (this.closed) return;
    const ws = new WebSocket(wsUrl(this.opts.path), protocols);
    this.ws = ws;
    let opened = false;
    ws.onopen = () => {
      opened = true;
      this.attempt = 0;
      this.handshakeFailures = 0;
      this.opts.onStatus?.("open");
    };
    ws.onmessage = (e) => {
      try {
        const data = JSON.parse(e.data) as F;
        this.opts.onFrame(data);
      } catch {
        // ignore non-JSON frames
      }
    };
    ws.onclose = (e) => {
      this.opts.onStatus?.("closed", { code: e.code, reason: e.reason });
      // 1000 (Normal) + 1001 (Going Away) are clean shutdowns; the
      // server explicitly signalled end-of-stream. Don't reconnect —
      // build streams end, that's the point. Auto-retry would re-ship
      // the archive and re-trigger phase=completed forever.
      if (e.code === 1000 || e.code === 1001) return;
      if (!opened) {
        void this.handleHandshakeFailure(e.code, e.reason);
        return;
      }
      this.scheduleReconnect();
    };
    ws.onerror = () => {
      this.opts.onStatus?.("error");
      // onclose will fire after onerror; let it handle backoff
    };
  }

  send(data: unknown) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(typeof data === "string" ? data : JSON.stringify(data));
    }
  }

  // close() is the caller tearing the stream down: detach handlers first
  // so the dying socket can't report a stale status or frame into
  // whatever the caller mounts next.
  close() {
    this.closed = true;
    if (this.timer) clearTimeout(this.timer);
    const ws = this.ws;
    this.ws = null;
    if (ws) {
      ws.onopen = null;
      ws.onmessage = null;
      ws.onclose = null;
      ws.onerror = null;
      ws.close();
    }
  }

  private async handleHandshakeFailure(code: number, reason: string) {
    this.handshakeFailures += 1;
    const max = this.opts.maxHandshakeFailures ?? 10;
    let verdict: "retry" | "stop" = this.handshakeFailures >= max ? "stop" : "retry";
    if (verdict === "retry" && this.opts.onHandshakeFailure) {
      try {
        verdict = await this.opts.onHandshakeFailure();
      } catch {
        verdict = "retry";
      }
    }
    if (this.closed) return;
    if (verdict === "stop") {
      this.closed = true;
      this.opts.onStatus?.("closed", { code, reason, gaveUp: true });
      return;
    }
    this.scheduleReconnect();
  }

  private scheduleReconnect() {
    if (this.closed) return;
    const max = this.opts.maxAttempts ?? Infinity;
    if (this.attempt >= max) return;
    const delay = Math.min(30_000, 500 * Math.pow(2, this.attempt));
    this.attempt += 1;
    this.timer = setTimeout(() => this.open(), delay);
  }
}

function wsUrl(path: string): string {
  if (typeof window === "undefined") return path;
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}${path}`;
}
