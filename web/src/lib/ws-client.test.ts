import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ReconnectingWS, type WSStatus } from "./ws-client";

// ---------------------------------------------------------------------------
// ReconnectingWS reconnect policy.
//
// The contract under test (ws-client.ts onclose handler):
//   - close codes 1000 (Normal) and 1001 (Going Away) are clean shutdowns
//     — the server ended the stream on purpose (build streams end!) and
//     the client must NOT reconnect, or it would re-ship the archive and
//     re-fire phase=completed forever.
//   - any other close (1006 abnormal, 1011 server error, …) schedules a
//     reconnect with capped exponential backoff (500ms · 2^attempt, max 30s).
//   - caller-initiated close() suppresses all reconnects.
//   - maxAttempts bounds the retries.
// ---------------------------------------------------------------------------

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;

  url: string;
  readyState = FakeWebSocket.CONNECTING;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onclose: ((e: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  send(data: string) {
    this.sent.push(data);
  }

  // close() from the client side; the browser then fires onclose. Use
  // serverClose() in tests to simulate the server dropping/ending us.
  close() {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code: 1000, reason: "client close" });
  }

  simulateOpen() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }

  serverClose(code: number, reason = "") {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code, reason });
  }
}

function lastSocket(): FakeWebSocket {
  return FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
}

function socketCount(): number {
  return FakeWebSocket.instances.length;
}

let statuses: Array<{ status: WSStatus; code?: number }>;

function makeWS(opts: { maxAttempts?: number; onFrame?: (f: unknown) => void } = {}) {
  return new ReconnectingWS({
    path: "/ws/projects/p/services/s/logs?env=production",
    onFrame: opts.onFrame ?? (() => {}),
    onStatus: (status, info) => statuses.push({ status, code: info?.code }),
    maxAttempts: opts.maxAttempts,
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  FakeWebSocket.instances = [];
  statuses = [];
  vi.stubGlobal("WebSocket", FakeWebSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("reconnect policy", () => {
  it("reconnects after an abnormal close (1006)", () => {
    const ws = makeWS();
    ws.open();
    expect(socketCount()).toBe(1);
    lastSocket().simulateOpen();

    lastSocket().serverClose(1006, "abnormal");
    expect(statuses.at(-1)).toEqual({ status: "closed", code: 1006 });

    // Backoff for attempt 0 is 500ms — not a tick sooner.
    vi.advanceTimersByTime(499);
    expect(socketCount()).toBe(1);
    vi.advanceTimersByTime(1);
    expect(socketCount()).toBe(2);
    expect(statuses.at(-1)?.status).toBe("connecting");
  });

  it("does NOT reconnect on a clean close (1000) — build streams end", () => {
    const ws = makeWS();
    ws.open();
    lastSocket().simulateOpen();

    lastSocket().serverClose(1000, "build complete");
    vi.advanceTimersByTime(120_000);
    expect(socketCount()).toBe(1); // no new socket, ever
    expect(statuses.at(-1)).toEqual({ status: "closed", code: 1000 });
  });

  it("does NOT reconnect on going-away (1001)", () => {
    const ws = makeWS();
    ws.open();
    lastSocket().simulateOpen();

    lastSocket().serverClose(1001);
    vi.advanceTimersByTime(120_000);
    expect(socketCount()).toBe(1);
  });

  it("reconnects on non-clean server-error codes (1011)", () => {
    const ws = makeWS();
    ws.open();
    lastSocket().simulateOpen();

    lastSocket().serverClose(1011, "internal error");
    vi.advanceTimersByTime(500);
    expect(socketCount()).toBe(2);
  });

  it("caller close() suppresses reconnection entirely", () => {
    const ws = makeWS();
    ws.open();
    lastSocket().simulateOpen();

    ws.close(); // fires onclose(1000) via the fake, and sets closed=true
    vi.advanceTimersByTime(120_000);
    expect(socketCount()).toBe(1);

    // Even open() after close() is a no-op.
    ws.open();
    expect(socketCount()).toBe(1);
  });

  it("caller close() cancels an already-scheduled reconnect", () => {
    const ws = makeWS();
    ws.open();
    lastSocket().simulateOpen();
    lastSocket().serverClose(1006); // reconnect timer armed (500ms)

    ws.close();
    vi.advanceTimersByTime(120_000);
    expect(socketCount()).toBe(1);
  });

  it("backoff doubles per attempt and resets once a connection opens", () => {
    const ws = makeWS();
    ws.open();
    lastSocket().simulateOpen();

    // Attempt 0 → 500ms.
    lastSocket().serverClose(1006);
    vi.advanceTimersByTime(500);
    expect(socketCount()).toBe(2);

    // Attempt 1 (never opened) → 1000ms.
    lastSocket().serverClose(1006);
    vi.advanceTimersByTime(999);
    expect(socketCount()).toBe(2);
    vi.advanceTimersByTime(1);
    expect(socketCount()).toBe(3);

    // A successful open resets the attempt counter → next delay is 500ms again.
    lastSocket().simulateOpen();
    lastSocket().serverClose(1006);
    vi.advanceTimersByTime(500);
    expect(socketCount()).toBe(4);
  });

  it("caps the backoff at 30s", () => {
    const ws = makeWS();
    ws.open();
    // 7 consecutive failures: delays 500,1k,2k,4k,8k,16k,30k(capped from 32k).
    const delays = [500, 1000, 2000, 4000, 8000, 16_000, 30_000];
    for (const d of delays) {
      const before = socketCount();
      lastSocket().serverClose(1006);
      vi.advanceTimersByTime(d - 1);
      expect(socketCount()).toBe(before);
      vi.advanceTimersByTime(1);
      expect(socketCount()).toBe(before + 1);
    }
  });

  it("stops after maxAttempts reconnects", () => {
    const ws = makeWS({ maxAttempts: 2 });
    ws.open();
    lastSocket().serverClose(1006);
    vi.advanceTimersByTime(500);
    expect(socketCount()).toBe(2); // attempt 1

    lastSocket().serverClose(1006);
    vi.advanceTimersByTime(1000);
    expect(socketCount()).toBe(3); // attempt 2

    lastSocket().serverClose(1006);
    vi.advanceTimersByTime(600_000);
    expect(socketCount()).toBe(3); // exhausted — no further sockets
  });
});

describe("frame + send plumbing", () => {
  it("delivers parsed JSON frames and ignores non-JSON", () => {
    const frames: unknown[] = [];
    const ws = makeWS({ onFrame: (f) => frames.push(f) });
    ws.open();
    lastSocket().simulateOpen();

    lastSocket().onmessage?.({ data: JSON.stringify({ line: "hello" }) });
    lastSocket().onmessage?.({ data: "not json at all" });
    lastSocket().onmessage?.({ data: JSON.stringify({ line: "world" }) });
    expect(frames).toEqual([{ line: "hello" }, { line: "world" }]);
  });

  it("send() only writes on an OPEN socket and serializes objects", () => {
    const ws = makeWS();
    ws.open();
    ws.send("dropped"); // still CONNECTING — silently dropped
    expect(lastSocket().sent).toEqual([]);

    lastSocket().simulateOpen();
    ws.send("plain");
    ws.send({ cmd: "resize", cols: 80 });
    expect(lastSocket().sent).toEqual(["plain", JSON.stringify({ cmd: "resize", cols: 80 })]);
  });
});
