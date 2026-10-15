import { describe, expect, it } from "vitest";
import { poolerForm, poolerPatch } from "./poolerPatch";

describe("poolerPatch", () => {
  const base = poolerForm({ enabled: true, poolSize: 10 });

  it("returns undefined when nothing changed", () => {
    expect(poolerPatch(base, base, false)).toBeUndefined();
  });
  it("sends only the changed pool size; blank resets to the default", () => {
    expect(poolerPatch({ ...base, poolSize: "" }, base, false)).toEqual({ pooler: { poolSize: 0 } });
  });
  it("pins externalBackend and sends host/port for external addons", () => {
    const ext = poolerForm({});
    expect(poolerPatch({ ...ext, enabled: true, host: "db.example.com", port: "6543" }, ext, true)).toEqual({
      pooler: { enabled: true, host: "db.example.com", port: 6543, externalBackend: true },
    });
  });
  it("refuses an external pooler without a host", () => {
    const ext = poolerForm({});
    expect(poolerPatch({ ...ext, enabled: true }, ext, true)).toHaveProperty("error");
  });
});
