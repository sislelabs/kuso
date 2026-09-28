import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import { getDefaultPodSize, listPodSizes, setDefaultPodSize, updateClusterSettings } from "./api";

describe("updateClusterSettings", () => {
  beforeEach(() => apiMock.mockReset());

  // POST /api/config 400s unless the spec sits under a "settings" key.
  it("wraps the spec in a settings envelope", async () => {
    apiMock.mockResolvedValue({ ok: true });
    await updateClusterSettings({ clusterissuer: "letsencrypt-staging" });
    expect(apiMock).toHaveBeenCalledWith("/api/config", {
      method: "POST",
      body: { settings: { clusterissuer: "letsencrypt-staging" } },
    });
  });
});

describe("default pod size", () => {
  beforeEach(() => apiMock.mockReset());

  it("reads the name out of the response", async () => {
    apiMock.mockResolvedValue({ name: "medium" });
    await expect(getDefaultPodSize()).resolves.toBe("medium");
    expect(apiMock).toHaveBeenCalledWith("/api/config/default-podsize");
  });

  it("PUTs the name", async () => {
    apiMock.mockResolvedValue({ name: "none" });
    await setDefaultPodSize("none");
    expect(apiMock).toHaveBeenCalledWith("/api/config/default-podsize", {
      method: "PUT",
      body: { name: "none" },
    });
  });

  // The Go handler encodes an empty table as JSON null.
  it("treats a null preset list as empty", async () => {
    apiMock.mockResolvedValue(null);
    await expect(listPodSizes()).resolves.toEqual([]);
  });
});
