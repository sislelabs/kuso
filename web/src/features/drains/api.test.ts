import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import { createDrain, deleteDrain, listDrains, parseHeaderLines, testDrain } from "./api";

describe("parseHeaderLines", () => {
  it("splits on the first colon or equals and skips blanks", () => {
    expect(parseHeaderLines("Authorization: Bearer a=b\n\nX-Scope-OrgID=tenant-1\n")).toEqual({
      ok: true,
      headers: { Authorization: "Bearer a=b", "X-Scope-OrgID": "tenant-1" },
    });
  });

  it("rejects a line with no separator", () => {
    const r = parseHeaderLines("Authorization Bearer x");
    expect(r.ok).toBe(false);
  });
});

describe("drains api", () => {
  beforeEach(() => apiMock.mockReset());

  // The Go handler encodes an empty slice as [], but guard null anyway.
  it("treats a null list as empty", async () => {
    apiMock.mockResolvedValue(null);
    await expect(listDrains()).resolves.toEqual([]);
    expect(apiMock).toHaveBeenCalledWith("/api/drains");
  });

  it("omits an empty project so the drain is instance-wide", async () => {
    apiMock.mockResolvedValue({ id: "d1" });
    await createDrain({ type: "otlp", url: "https://otlp.example.com", project: "", headers: {}, secret: "" });
    expect(apiMock).toHaveBeenCalledWith("/api/drains", {
      method: "POST",
      body: { type: "otlp", url: "https://otlp.example.com" },
    });
  });

  it("encodes ids in paths", async () => {
    apiMock.mockResolvedValue({ ok: true, status: 204 });
    await testDrain("a/b");
    expect(apiMock).toHaveBeenCalledWith("/api/drains/a%2Fb/test", { method: "POST" });
    await deleteDrain("a/b");
    expect(apiMock).toHaveBeenCalledWith("/api/drains/a%2Fb", { method: "DELETE" });
  });
});
