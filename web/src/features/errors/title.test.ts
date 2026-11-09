import { describe, expect, it } from "vitest";
import { errorTitle } from "./title";

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
