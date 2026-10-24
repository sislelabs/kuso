import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import { listAddonPods, listServicePods } from "./api";

describe("pods api", () => {
  beforeEach(() => apiMock.mockReset());

  it("passes the env CR name through", async () => {
    apiMock.mockResolvedValue({ namespace: "p", pods: [] });
    await listServicePods("p", "web", "p-web-pr-7");
    expect(apiMock).toHaveBeenCalledWith("/api/projects/p/services/web/pods?env=p-web-pr-7");
  });

  it("hits the addon pods route", async () => {
    apiMock.mockResolvedValue({ namespace: "p", pods: [] });
    await listAddonPods("p", "db");
    expect(apiMock).toHaveBeenCalledWith("/api/projects/p/addons/db/pods");
  });
});
