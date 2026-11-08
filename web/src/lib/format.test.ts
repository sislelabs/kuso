import { describe, expect, it } from "vitest";
import { formatCpu, formatLatency, relativeTime } from "./format";

const now = Date.parse("2026-10-01T12:00:00Z");
const at = (offsetSec: number) => new Date(now + offsetSec * 1000).toISOString();

describe("relativeTime", () => {
  it("renders the past as 'ago'", () => {
    expect(relativeTime(at(-30), now)).toBe("30s ago");
    expect(relativeTime(at(-3 * 3600), now)).toBe("3h ago");
  });

  it("renders the future as 'in …', never a negative 'ago'", () => {
    expect(relativeTime(at(7 * 86400), now)).toBe("in 7d");
    expect(relativeTime(at(90 * 60), now)).toBe("in 1h");
  });

  it("treats small clock skew as just now", () => {
    expect(relativeTime(at(3), now)).toBe("just now");
    expect(relativeTime(at(-2), now)).toBe("just now");
  });
});

describe("formatCpu", () => {
  it("uses mCPU below one core and cores above", () => {
    expect(formatCpu(0)).toBe("0 mCPU");
    expect(formatCpu(0.4)).toBe("<1 mCPU");
    expect(formatCpu(3)).toBe("3 mCPU");
    expect(formatCpu(250.4)).toBe("250 mCPU");
    expect(formatCpu(1000)).toBe("1 core");
    expect(formatCpu(1340)).toBe("1.3 cores");
    expect(formatCpu(12_400)).toBe("12 cores");
  });
});

describe("formatLatency", () => {
  it("reads ms then s", () => {
    expect(formatLatency(1)).toBe("1 ms");
    expect(formatLatency(4.6)).toBe("5 ms");
    expect(formatLatency(1200)).toBe("1.2 s");
  });
});
