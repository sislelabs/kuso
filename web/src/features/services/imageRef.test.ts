import { describe, expect, it } from "vitest";
import { parseImageRef } from "./imageRef";

describe("parseImageRef", () => {
  it("splits repository and tag", () => {
    expect(parseImageRef("ghcr.io/acme/api:1.4.2")).toEqual({ repository: "ghcr.io/acme/api", tag: "1.4.2", name: "api" });
    expect(parseImageRef(" nginx ")).toEqual({ repository: "nginx", tag: "", name: "nginx" });
  });
  it("doesn't read a registry port as a tag and accepts digests", () => {
    expect(parseImageRef("localhost:5000/team/app")).toEqual({ repository: "localhost:5000/team/app", tag: "", name: "app" });
    const digest = `sha256:${"a".repeat(64)}`;
    expect(parseImageRef(`ghcr.io/acme/api@${digest}`)?.tag).toBe(digest);
  });
  it("rejects what a registry wouldn't take", () => {
    expect(parseImageRef("")).toBeNull();
    expect(parseImageRef("Upper/Case")).toBeNull();
    expect(parseImageRef("ghcr.io/acme/api:bad tag")).toBeNull();
    expect(parseImageRef("ghcr.io/acme/api@sha256:short")).toBeNull();
  });
});
