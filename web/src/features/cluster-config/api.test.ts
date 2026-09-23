import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import { updateClusterSettings } from "./api";

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
