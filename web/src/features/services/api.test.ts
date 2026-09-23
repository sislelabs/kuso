import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import { getServiceEnvOverrides } from "./api";

describe("getServiceEnvOverrides", () => {
  beforeEach(() => apiMock.mockReset());

  // Without ?env= the server returns the service-wide list, so a
  // staging-only override set via `kuso env set --env staging` never shows.
  it("asks for the selected environment's own overrides", async () => {
    apiMock.mockResolvedValue({ envVars: [] });
    await getServiceEnvOverrides("shop", "api", "staging");
    expect(apiMock).toHaveBeenCalledWith("/api/projects/shop/services/api/env?env=staging");
  });
});
