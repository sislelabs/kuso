import { describe, expect, it } from "vitest";
import { collapseEvents, type ServicePodEvent } from "./podEvents";

const ev = (o: Partial<ServicePodEvent>): ServicePodEvent => ({
  type: "Warning",
  reason: "Failed",
  message: "Failed to pull image",
  object: "pod/web-1",
  count: 1,
  ...o,
});

describe("collapseEvents", () => {
  it("merges rows that read the same, summing counts and widening the range", () => {
    const out = collapseEvents([
      ev({ summary: "The image can't be pulled", message: "rpc error: not found", count: 2, firstSeen: "2026-01-01T10:01:00Z", lastSeen: "2026-01-01T10:05:00Z" }),
      ev({ summary: "The image can't be pulled", message: "Error: ErrImagePull", count: 3, firstSeen: "2026-01-01T10:00:00Z", lastSeen: "2026-01-01T10:04:00Z" }),
    ]);
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ count: 5, firstSeen: "2026-01-01T10:00:00Z", lastSeen: "2026-01-01T10:05:00Z" });
  });

  it("keeps different reasons, objects and wording apart", () => {
    const out = collapseEvents([
      ev({}),
      ev({ reason: "BackOff" }),
      ev({ object: "pod/web-2" }),
      ev({ message: "Failed to pull image: timeout" }),
    ]);
    expect(out).toHaveLength(4);
  });

  it("doesn't mutate the input", () => {
    const input = [ev({ count: 1 }), ev({ count: 1 })];
    collapseEvents(input);
    expect(input[0].count).toBe(1);
  });
});
