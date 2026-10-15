import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import { AVATAR_MAX_BYTES, resetUserPassword, uploadAvatar, validateAvatarFile } from "./api";

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

describe("avatar upload", () => {
  beforeEach(() => apiMock.mockReset());

  it("accepts the four image types the server sniffs for", () => {
    for (const type of ["image/png", "image/jpeg", "image/gif", "image/webp"]) {
      expect(validateAvatarFile({ size: 10, type })).toBeNull();
    }
    expect(validateAvatarFile({ size: 10, type: "image/svg+xml" })).toMatch(/PNG, JPEG, GIF or WEBP/);
  });

  // Over the cap the router's 1 MiB body limit fails the multipart parse
  // with a bare "bad form"; catch it before upload with a useful message.
  it("rejects files over the size cap", () => {
    expect(validateAvatarFile({ size: AVATAR_MAX_BYTES + 1, type: "image/png" })).toMatch(/limit/);
  });

  it("posts multipart with the avatar field", async () => {
    apiMock.mockResolvedValue({ ok: true });
    await uploadAvatar(new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }));
    const [path, opts] = apiMock.mock.calls[0] as [string, { method: string; body: FormData }];
    expect(path).toBe("/api/users/profile/avatar");
    expect(opts.method).toBe("POST");
    expect(opts.body.get("avatar")).toBeInstanceOf(Blob);
  });
});
