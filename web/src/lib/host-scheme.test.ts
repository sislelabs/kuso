import { describe, expect, it } from "vitest";
import { hostScheme } from "./host-scheme";

describe("hostScheme", () => {
  it("is https for a public host with TLS on", () => {
    expect(hostScheme(true, "api.shop.example.org")).toBe("https");
  });
  it("is http when TLS is off", () => {
    expect(hostScheme(false, "api.shop.example.org")).toBe("http");
  });
  it("is http for hosts the chart never gets a cert for", () => {
    for (const h of ["api.shop.kuso.localhost", "API.corp.INTERNAL", "x.test", "x.local", "x.localhost."]) {
      expect(hostScheme(true, h)).toBe("http");
    }
  });
});
