import { describe, expect, it } from "vitest";
import { errorTitle, groupTitle } from "./title";

describe("errorTitle", () => {
  it("drops a leading timestamp and level", () => {
    expect(errorTitle("2026-11-08T19:40:05Z ERROR failed to send order receipt order_id=1012")).toBe(
      "failed to send order receipt order_id=1012",
    );
    expect(errorTitle("[2026-11-08 19:40:05.123] [error] boom")).toBe("boom");
    expect(errorTitle("ERROR: disk full")).toBe("disk full");
  });
  it("keeps messages that are only a level, and leaves others alone", () => {
    expect(errorTitle("ERROR")).toBe("ERROR");
    expect(errorTitle("TypeError: x is undefined")).toBe("TypeError: x is undefined");
    expect(errorTitle("Errors happen")).toBe("Errors happen");
  });
  it("names a traceback after its exception line, never the bare header", () => {
    const sample = 'Traceback (most recent call last):\n  File "w.py", line 9, in run\nKeyError: \'cart\'';
    expect(errorTitle("Traceback (most recent call last):", sample)).toBe("KeyError: 'cart'");
    expect(errorTitle("Traceback (most recent call last):")).toBe("Python traceback");
  });
});

describe("groupTitle", () => {
  it("masks the ids one occurrence carried", () => {
    expect(groupTitle("2026-11-09T10:00:00Z ERROR payment webhook failed: stripe: read order_id=1006")).toBe(
      "payment webhook failed: stripe: read order_id=…",
    );
    expect(groupTitle('job failed requestId: "a1b2" user id=42')).toBe('job failed requestId: "…" user id=…');
    expect(groupTitle("lost 3fa85f64-5717-4562-b3fc-2c963f66afa6 at 0xc0001a2b")).toBe("lost … at …");
    expect(groupTitle("upstream *109 connect() failed for order #1012")).toBe("upstream *109 connect() failed for order #…");
  });
  it("leaves measurements and words alone", () => {
    expect(groupTitle("timeout after 3000ms (attempt 3)")).toBe("timeout after 3000ms (attempt 3)");
    expect(groupTitle("invalid id format in paid=true")).toBe("invalid id format in paid=true");
  });
});
