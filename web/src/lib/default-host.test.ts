import { describe, expect, it } from "vitest";
import { defaultServiceHost } from "./default-host";

describe("defaultServiceHost", () => {
  it("nests the service under the project on the instance domain", () => {
    expect(defaultServiceHost("web", "shop", "", "kuso.example.com")).toBe(
      "web.shop.kuso.example.com"
    );
  });

  it("drops the service label when it matches the project (instance domain)", () => {
    expect(defaultServiceHost("shop", "shop", "", "kuso.example.com")).toBe(
      "shop.kuso.example.com"
    );
  });

  it("puts the service directly under a project base domain", () => {
    expect(defaultServiceHost("web", "shop", "shop.example.com", "kuso.example.com")).toBe(
      "web.shop.example.com"
    );
  });

  it("serves at the bare base domain when service matches the project", () => {
    expect(defaultServiceHost("shop", "shop", ".shop.example.com.", "kuso.example.com")).toBe(
      "shop.example.com"
    );
  });
});
