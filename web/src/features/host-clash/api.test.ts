import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import { clashingHosts, listIngressDomains, normalizeHost } from "./api";

describe("normalizeHost", () => {
  it("strips scheme, path, port, case and trailing dots", () => {
    expect(normalizeHost(" HTTPS://App.Example.com:443/path ")).toBe("app.example.com");
    expect(normalizeHost("app.example.com.")).toBe("app.example.com");
  });
});

describe("clashingHosts", () => {
  it("returns only exact, de-duplicated matches", () => {
    const taken = ["api.example.com", "*.example.com"];
    expect(clashingHosts(["https://API.example.com/", "api.example.com", "web.example.com", ""], taken)).toEqual([
      "api.example.com",
    ]);
  });
});

describe("listIngressDomains", () => {
  beforeEach(() => apiMock.mockReset());
  it("treats a null body as no domains", async () => {
    apiMock.mockResolvedValue(null);
    await expect(listIngressDomains()).resolves.toEqual([]);
    expect(apiMock).toHaveBeenCalledWith("/api/kubernetes/domains");
  });
});
