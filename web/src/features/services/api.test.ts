import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import { getServiceEnvOverrides, wakeService } from "./api";

describe("getServiceEnvOverrides", () => {
  beforeEach(() => apiMock.mockReset());

  // Without ?env= the server returns the service-wide list, so a
  // staging-only override set via `kuso env set --env staging` never shows.
  it("asks for the selected environment's own overrides", async () => {
    apiMock.mockResolvedValue({ envVars: [] });
    await getServiceEnvOverrides("shop", "api", "staging");
    expect(apiMock).toHaveBeenCalledWith("/api/projects/shop/services/api/env?env=staging&reveal=true");
  });
});

describe("wakeService", () => {
  beforeEach(() => apiMock.mockReset());

  it("wakes production without ?env= and another env with it", async () => {
    apiMock.mockResolvedValue(undefined);
    await wakeService("shop", "api");
    await wakeService("shop", "api", "shop-api-pr-7");
    expect(apiMock.mock.calls.map((c) => c[0])).toEqual([
      "/api/projects/shop/services/api/wake",
      "/api/projects/shop/services/api/wake?env=shop-api-pr-7",
    ]);
  });
});
