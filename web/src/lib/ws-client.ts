// Reconnecting WebSocket wrapper with JWT auth via subprotocol.
// Use case: log tail streaming. Caller drives via onFrame/onStatus
// callbacks; the wrapper handles auto-reconnect with capped exponential
// backoff and surfaces transient errors as "disconnected" status.


export type WSStatus = "connecting" | "open" | "closed" | "error";

export interface WSOptions<F = unknown> {
  /** Path on the kuso server, e.g. /ws/projects/foo/services/bar/logs?env=production */
  path: string;
  onFrame: (frame: F) => void;
  onStatus?: (status: WSStatus, info?: { code?: number; reason?: string }) => void;
  /** Max reconnect attempts before giving up. Default Infinity. */
  maxAttempts?: number;
}

export class ReconnectingWS<F = unknown> {
  private opts: WSOptions<F>;
  private ws: WebSocket | null = null;
  private attempt = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private closed = false;

  constructor(opts: WSOptions<F>) {
    this.opts = opts;
  }

  open() {
    if (this.closed) return;
    this.opts.onStatus?.("connecting");
    const url = wsUrl(this.opts.path);
    // Cookie-mode auth: the browser carries kuso.JWT_TOKEN on the
    // upgrade request automatically. The server's logs_ws handler
    // falls through from Sec-WebSocket-Protocol to the cookie when
    // the protocol slot is empty.
    const ws = new WebSocket(url);
    this.ws = ws;
    ws.onopen = () => {
      this.attempt = 0;
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

  close() {
    this.closed = true;
    if (this.timer) clearTimeout(this.timer);
    if (this.ws) this.ws.close();
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
