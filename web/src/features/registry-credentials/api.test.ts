import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import {
  listRegistryCredentials,
  loginRegistry,
  logoutRegistry,
} from "./api";

describe("registry credentials api", () => {
  beforeEach(() => apiMock.mockReset());

  it("lists and tolerates a null list", async () => {
    apiMock.mockResolvedValue({ credentials: null });
    await expect(listRegistryCredentials("shop")).resolves.toEqual([]);
    expect(apiMock).toHaveBeenCalledWith("/api/projects/shop/registry-credentials");
  });

  it("posts the login body", async () => {
    apiMock.mockResolvedValue({ registry: "ghcr.io", username: "octo", secretName: "s" });
    await loginRegistry("shop", { registry: "ghcr.io", username: "octo", password: "pw" });
    expect(apiMock).toHaveBeenCalledWith("/api/projects/shop/registry-credentials", {
      method: "POST",
      body: { registry: "ghcr.io", username: "octo", password: "pw" },
    });
  });

  it("encodes the registry host in the logout path", async () => {
    apiMock.mockResolvedValue(undefined);
    await logoutRegistry("shop", "localhost:5000/x");
    expect(apiMock).toHaveBeenCalledWith(
      "/api/projects/shop/registry-credentials/localhost%3A5000%2Fx",
      { method: "DELETE" },
    );
  });
});
