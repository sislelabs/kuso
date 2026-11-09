import { describe, expect, it } from "vitest";
import { firstServiceHost } from "./default-service-name";

describe("firstServiceHost", () => {
  it("names the host after the service the image path creates", () => {
    expect(firstServiceHost("shop", "nginx", "", "apps.example.com")).toBe("shop.apps.example.com");
    expect(firstServiceHost("shop", "nginx", "shop.dev", undefined)).toBe("shop.dev");
  });

  it("falls back to the web service when the project name is too long for a service", () => {
    const long = "a-very-long-project-name-that-blows-the-helm-budget-x";
    expect(firstServiceHost(long, "nginx", "", "apps.example.com")).toBe(`web.${long}.apps.example.com`);
  });

  it("is unknown without a base domain or the server's domain", () => {
    expect(firstServiceHost("shop", "nginx", "", undefined)).toBeUndefined();
  });
});
