import { describe, expect, it } from "vitest";
import { createReplayFilter } from "./hooks";

const line = (pod: string, ts: string, text: string) => ({ pod, ts, line: text });

describe("createReplayFilter", () => {
  it("drops the tail a reconnect replays, keeps the genuinely new lines", () => {
    const accept = createReplayFilter();
    const first = [
      line("web-a", "2026-10-01T10:00:00.100000000Z", "boot"),
      line("web-a", "2026-10-01T10:00:01.200000000Z", "ready"),
      line("web-b", "2026-10-01T10:00:01.300000000Z", "ready"),
    ];
    expect(first.map(accept)).toEqual([true, true, true]);

    // Reconnect: server re-sends ?tail=200 for each pod, then new lines.
    const replay = [...first, line("web-a", "2026-10-01T10:00:05.000000000Z", "GET /")];
    expect(replay.map(accept)).toEqual([false, false, false, true]);
  });

  it("keeps distinct lines that share a timestamp, drops their replay", () => {
    const accept = createReplayFilter();
    const a = line("p", "2026-10-01T10:00:00.5Z", "one");
    const b = line("p", "2026-10-01T10:00:00.5Z", "two");
    expect([accept(a), accept(b)]).toEqual([true, true]);
    expect([accept(a), accept(b)]).toEqual([false, false]);
  });

  it("never filters one pod by another pod's clock", () => {
    const accept = createReplayFilter();
    expect(accept(line("p", "2026-10-01T10:00:00Z", "new pod"))).toBe(true);
    expect(accept(line("q", "2026-10-01T09:00:00Z", "older pod"))).toBe(true);
  });

  it("lets lines without a usable timestamp through", () => {
    const accept = createReplayFilter();
    expect(accept({ pod: "p", line: "x" })).toBe(true);
    expect(accept({ pod: "p", line: "x" })).toBe(true);
  });
});
