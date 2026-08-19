import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "./api-client";

// ---------------------------------------------------------------------------
// ApiError — tiered friendly-message extraction.
//
// The server speaks three error shapes:
//   1. plain text body      → "addon foo/bar already exists" (409 conflicts)
//   2. JSON {error: "..."}  → handler fail() helpers
//   3. JSON {message: "..."}→ some upstream/proxy shapes
// The constructor must pick the most useful message, in that order, and
// fall back to the bare "<status> <statusText>" only when nothing better
// exists.
// ---------------------------------------------------------------------------

describe("ApiError message extraction", () => {
  it("prefers a non-empty string body (conflict passthrough)", () => {
    const e = new ApiError(409, "addon foo/bar already exists", "409 Conflict");
    expect(e.message).toBe("addon foo/bar already exists");
    expect(e.status).toBe(409);
    expect(e.body).toBe("addon foo/bar already exists");
  });

  it("trims the string body", () => {
    const e = new ApiError(409, "  spaced out  \n", "409 Conflict");
    expect(e.message).toBe("spaced out");
  });

  it("falls back past an empty/whitespace-only string body", () => {
    expect(new ApiError(500, "", "500 Internal Server Error").message).toBe(
      "500 Internal Server Error"
    );
    expect(new ApiError(500, "   \n\t", "500 Internal Server Error").message).toBe(
      "500 Internal Server Error"
    );
  });

  it("unwraps {error: string}", () => {
    const e = new ApiError(400, { error: "name is required" }, "400 Bad Request");
    expect(e.message).toBe("name is required");
  });

  it("unwraps {message: string}", () => {
    const e = new ApiError(400, { message: "invalid spec" }, "400 Bad Request");
    expect(e.message).toBe("invalid spec");
  });

  it("prefers .error over .message when both are present", () => {
    const e = new ApiError(
      422,
      { error: "from error", message: "from message" },
      "422 Unprocessable Entity"
    );
    expect(e.message).toBe("from error");
  });

  it("skips a non-string .error and uses a string .message", () => {
    const e = new ApiError(
      500,
      { error: { code: 12 }, message: "the real text" },
      "500 Internal Server Error"
    );
    expect(e.message).toBe("the real text");
  });

  it("falls back to statusText for shapes with nothing usable", () => {
    for (const body of [
      null,
      undefined,
      42,
      true,
      [],
      ["not", "an", "object with error"],
      {},
      { error: 123 },
      { message: null },
      { detail: "unrecognized key" },
    ]) {
      const e = new ApiError(503, body, "503 Service Unavailable");
      expect(e.message).toBe("503 Service Unavailable");
      expect(e.body).toBe(body); // raw body always preserved for callers
    }
  });

  it("surfaces the envelope's machine code as .code", () => {
    const e = new ApiError(
      404,
      { error: "service not found", code: "not_found" },
      "404 Not Found"
    );
    expect(e.message).toBe("service not found");
    expect(e.code).toBe("not_found");
  });

  it("leaves .code undefined for non-envelope bodies", () => {
    expect(new ApiError(409, "plain text conflict", "409 Conflict").code).toBeUndefined();
    expect(new ApiError(400, { error: "x" }, "400 Bad Request").code).toBeUndefined();
    expect(new ApiError(400, { error: "x", code: 42 }, "400 Bad Request").code).toBeUndefined();
  });

  it("is an Error with the status attached", () => {
    const e = new ApiError(404, "not found", "404 Not Found");
    expect(e).toBeInstanceOf(Error);
    expect(e).toBeInstanceOf(ApiError);
    expect(e.status).toBe(404);
  });
});

// ---------------------------------------------------------------------------
// api() — response handling around the extraction (fetch stubbed).
// ---------------------------------------------------------------------------

function stubFetch(
  status: number,
  body: string,
  contentType = "application/json",
  statusText = ""
) {
  const fn = vi.fn(async () =>
    new Response(status === 204 ? null : body, {
      status,
      statusText,
      headers: { "Content-Type": contentType },
    })
  );
  vi.stubGlobal("fetch", fn);
  return fn;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("api()", () => {
  it("returns parsed JSON on success", async () => {
    stubFetch(200, JSON.stringify({ ok: true, n: 3 }));
    await expect(api("/api/x")).resolves.toEqual({ ok: true, n: 3 });
  });

  it("returns undefined on 204", async () => {
    stubFetch(204, "");
    await expect(api("/api/x")).resolves.toBeUndefined();
  });

  it("throws ApiError carrying the text body on a text/plain 409", async () => {
    stubFetch(409, "addon foo/bar already exists", "text/plain");
    const err = (await api("/api/x").catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(409);
    expect(err.message).toBe("addon foo/bar already exists");
  });

  it("throws ApiError with the JSON error field unwrapped", async () => {
    stubFetch(400, JSON.stringify({ error: "branch must not contain slashes" }));
    const err = (await api("/api/x").catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.message).toBe("branch must not contain slashes");
    expect(err.body).toEqual({ error: "branch must not contain slashes" });
  });

  it("treats a non-JSON error body as text, not a parse crash", async () => {
    stubFetch(502, "<html>bad gateway</html>", "text/html");
    const err = (await api("/api/x").catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.message).toBe("<html>bad gateway</html>");
  });

  it("parses the full server envelope {error, code} end to end", async () => {
    stubFetch(
      409,
      JSON.stringify({ error: "addon foo/bar already exists", code: "conflict" })
    );
    const err = (await api("/api/x").catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(409);
    expect(err.message).toBe("addon foo/bar already exists");
    expect(err.code).toBe("conflict");
    expect(err.body).toEqual({
      error: "addon foo/bar already exists",
      code: "conflict",
    });
  });

  it("keeps extra envelope fields (shadowed-secret hint) on .body", async () => {
    stubFetch(
      409,
      JSON.stringify({
        error: "DATABASE_URL is shadowed by a project-scoped secret",
        code: "shadowed",
        key: "DATABASE_URL",
        scope: "project",
      })
    );
    const err = (await api("/api/x").catch((e: unknown) => e)) as ApiError;
    expect(err.code).toBe("shadowed");
    expect((err.body as { key: string }).key).toBe("DATABASE_URL");
  });

  it("falls back to '<status> <statusText>' when the error body is empty", async () => {
    stubFetch(500, "", "application/json", "Internal Server Error");
    const err = (await api("/api/x").catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.message).toBe("500 Internal Server Error");
  });
});
