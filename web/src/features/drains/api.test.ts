import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import {
  DRAIN_MASK,
  buildDrainUpdate,
  createDrain,
  deleteDrain,
  drainToEditForm,
  listDrains,
  parseHeaderLines,
  testDrain,
  updateDrain,
  type Drain,
} from "./api";

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

describe("drain edit", () => {
  const stored: Drain = {
    id: "d1",
    name: "grafana",
    type: "loki",
    url: "https://logs.example.com",
    project: "",
    headers: { Authorization: DRAIN_MASK, "X-Scope-OrgID": DRAIN_MASK },
    secret: DRAIN_MASK,
    enabled: true,
    createdAt: "",
    updatedAt: "",
  };

  // Round-tripping the masked read must not overwrite the real
  // credentials with the mask: the server keeps a value only when it
  // gets the sentinel back.
  it("sends masked headers and an untouched secret back as the sentinel", () => {
    const r = buildDrainUpdate(stored, { ...drainToEditForm(stored), url: " https://new.example.com " });
    expect(r).toEqual({
      ok: true,
      body: {
        name: "grafana",
        type: "loki",
        url: "https://new.example.com",
        project: "",
        headers: { Authorization: DRAIN_MASK, "X-Scope-OrgID": DRAIN_MASK },
        secret: DRAIN_MASK,
        enabled: true,
      },
    });
  });

  it("drops a header removed from the text and clears the secret on request", () => {
    const f = { ...drainToEditForm(stored), headersText: `Authorization: ${DRAIN_MASK}`, clearSecret: true };
    const r = buildDrainUpdate(stored, f);
    expect(r.ok && r.body.headers).toEqual({ Authorization: DRAIN_MASK });
    expect(r.ok && r.body.secret).toBe("");
  });

  it("sends a newly typed secret verbatim", () => {
    const r = buildDrainUpdate(stored, { ...drainToEditForm(stored), secret: "new" });
    expect(r.ok && r.body.secret).toBe("new");
  });

  it("leaves the secret empty when none was stored", () => {
    const r = buildDrainUpdate({ ...stored, secret: "" }, drainToEditForm({ ...stored, secret: "" }));
    expect(r.ok && r.body.secret).toBe("");
  });

  it("PUTs to the drain id", async () => {
    apiMock.mockReset();
    apiMock.mockResolvedValue({ id: "d1" });
    const r = buildDrainUpdate(stored, drainToEditForm(stored));
    if (!r.ok) throw new Error(r.error);
    await updateDrain("d1", r.body);
    expect(apiMock).toHaveBeenCalledWith("/api/drains/d1", { method: "PUT", body: r.body });
  });
});
