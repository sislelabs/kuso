import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import { resetUserPassword } from "./api";

describe("resetUserPassword", () => {
  beforeEach(() => apiMock.mockReset());

  // PUT /api/users/id/{id} ignores `password` and returns 204, so hitting
  // it looked like a successful reset while the old password kept working.
  it("PUTs to the admin password route, not the generic user update", async () => {
    apiMock.mockResolvedValue(undefined);
    await resetUserPassword("u/1", "hunter22");
    expect(apiMock).toHaveBeenCalledWith("/api/users/id/u%2F1/password", {
      method: "PUT",
      body: { password: "hunter22" },
    });
  });
});
