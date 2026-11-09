import { describe, expect, it } from "vitest";
import { displayImageTag, imageRefString, imageTagProblem, parseImageRef, specImageTag, splitPastedImage } from "./imageRef";

const digest = `sha256:${"a".repeat(64)}`;

describe("parseImageRef", () => {
  it("splits repository and tag", () => {
    expect(parseImageRef("ghcr.io/acme/api:1.4.2")).toEqual({ repository: "ghcr.io/acme/api", tag: "1.4.2", name: "api" });
    expect(parseImageRef(" nginx ")).toEqual({ repository: "nginx", tag: "", name: "nginx" });
  });
  it("doesn't read a registry port as a tag and accepts digests", () => {
    expect(parseImageRef("localhost:5000/team/app")).toEqual({ repository: "localhost:5000/team/app", tag: "", name: "app" });
    expect(parseImageRef(`ghcr.io/acme/api@${digest}`)?.tag).toBe(digest);
  });
  it("rejects what a registry wouldn't take", () => {
    expect(parseImageRef("")).toBeNull();
    expect(parseImageRef("Upper/Case")).toBeNull();
    expect(parseImageRef("ghcr.io/acme/api:bad tag")).toBeNull();
    expect(parseImageRef("ghcr.io/acme/api@sha256:short")).toBeNull();
  });
});

describe("splitPastedImage", () => {
  it("splits owner/img:tag, digests, tag@digest and registry ports", () => {
    expect(splitPastedImage("owner/img:v2")).toEqual({ repository: "owner/img", tag: "v2" });
    expect(splitPastedImage(`img@${digest}`)).toEqual({ repository: "img", tag: digest });
    expect(splitPastedImage(`ghcr.io/a/b:1.0@${digest}`)).toEqual({ repository: "ghcr.io/a/b", tag: `1.0@${digest}` });
    expect(splitPastedImage("host:5000/img:tag")).toEqual({ repository: "host:5000/img", tag: "tag" });
  });
  it("leaves a bare repository alone, port included", () => {
    expect(splitPastedImage("host:5000/img")).toBeNull();
    expect(splitPastedImage("ghcr.io/acme/api")).toBeNull();
  });
});

describe("image tag field", () => {
  it("is required and validated", () => {
    expect(imageTagProblem("")).toMatch(/Enter a tag/);
    expect(imageTagProblem("bad tag")).not.toBeNull();
    expect(imageTagProblem("sha256:abc")).toMatch(/digest/);
    expect(imageTagProblem("1.4.2")).toBeNull();
    expect(imageTagProblem(digest)).toBeNull();
    expect(imageTagProblem(`v1@${digest}`)).toBeNull();
  });
  it("round-trips a bare digest through the spec's tag", () => {
    expect(specImageTag(digest)).toBe(`latest@${digest}`);
    expect(displayImageTag(specImageTag(digest))).toBe(digest);
    expect(specImageTag("v1")).toBe("v1");
    expect(imageRefString("img", digest)).toBe(`img@${digest}`);
    expect(imageRefString("img", "v1")).toBe("img:v1");
  });
});
