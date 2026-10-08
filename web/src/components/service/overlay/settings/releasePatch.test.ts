import { describe, expect, it } from "vitest";
import { releasePatch } from "./releasePatch";

const argv = ["sh", "-c", "npx payload migrate && node seed.js"];
const joined = argv.join(" ");

describe("releasePatch", () => {
  it("keeps a quoted argv intact on a timeout-only change", () => {
    expect(releasePatch(joined, "1200", joined, "900", argv)).toEqual({
      command: argv,
      timeoutSeconds: 1200,
    });
  });

  it("re-splits the command when the text was edited", () => {
    expect(releasePatch("bin/migrate --up", "", joined, "900", argv)).toEqual({
      command: ["bin/migrate", "--up"],
      timeoutSeconds: 0,
    });
  });

  it("clears an existing hook when the command is emptied", () => {
    expect(releasePatch("  ", "900", joined, "900", argv)).toEqual({ clear: true });
  });

  it("sends nothing when unchanged or when there was never a hook", () => {
    expect(releasePatch(joined, "900", joined, "900", argv)).toBeUndefined();
    expect(releasePatch("", "60", "", "", undefined)).toBeUndefined();
  });
});
