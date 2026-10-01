import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api-client";
import { loginErrorMessage } from "./loginError";

describe("loginErrorMessage", () => {
  it("distinguishes failures instead of collapsing them", () => {
    expect(loginErrorMessage(new ApiError(401, { error: "unauthorized" }, "Unauthorized"))).toBe(
      "invalid credentials",
    );
    expect(loginErrorMessage(new ApiError(429, { error: "too many requests" }, ""))).toMatch(/too many/);
    expect(loginErrorMessage(new ApiError(502, "", "Bad Gateway"))).toMatch(/server error \(502\)/);
    expect(loginErrorMessage(new ApiError(400, { error: "bad request: x" }, ""))).toBe("bad request: x");
    expect(loginErrorMessage(new TypeError("Failed to fetch"))).toBe("couldn't reach the server");
  });
});
