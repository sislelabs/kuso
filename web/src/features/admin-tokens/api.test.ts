import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import { issueTokenForUser, listAllTokens, listTokenUserOptions, revokeAnyToken } from "./api";

describe("admin tokens api", () => {
  beforeEach(() => apiMock.mockReset());

  it("treats a null list as empty", async () => {
    apiMock.mockResolvedValue(null);
    await expect(listAllTokens()).resolves.toEqual([]);
    expect(apiMock).toHaveBeenCalledWith("/api/tokens");
  });

  it("issues against the target user's id, escaped", async () => {
    apiMock.mockResolvedValue({ name: "ci", token: "jwt", expiresAt: "2027-01-01T00:00:00Z" });
    await issueTokenForUser("u/1", "ci", "");
    expect(apiMock).toHaveBeenCalledWith("/api/tokens/user/u%2F1", {
      method: "POST",
      body: { name: "ci", expiresAt: "" },
    });
  });

  // DELETE /api/tokens/my/{id} only matches the caller's own rows; the
  // admin route is the one that can revoke anybody's.
  it("revokes through the admin route, not the self route", async () => {
    apiMock.mockResolvedValue(undefined);
    await revokeAnyToken("t1");
    expect(apiMock).toHaveBeenCalledWith("/api/tokens/t1", { method: "DELETE" });
  });

  it("accepts both user-list shapes", async () => {
    const u = { id: "1", username: "a", email: "a@x", firstName: "A" };
    apiMock.mockResolvedValueOnce({ data: [u] });
    await expect(listTokenUserOptions()).resolves.toEqual([{ id: "1", username: "a", email: "a@x" }]);
    apiMock.mockResolvedValueOnce([u]);
    await expect(listTokenUserOptions()).resolves.toEqual([{ id: "1", username: "a", email: "a@x" }]);
  });
});
