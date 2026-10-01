import { describe, expect, it } from "vitest";
import { relativeTime } from "./format";

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
