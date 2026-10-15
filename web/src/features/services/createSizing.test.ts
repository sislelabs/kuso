import { describe, expect, it } from "vitest";
import { sizingBody } from "./createSizing";

const presets = [{ Name: "small", CPURequest: "100m", CPULimit: "", MemoryRequest: "128Mi", MemoryLimit: "512Mi" }];

describe("sizingBody", () => {
  it("omits everything by default so the server applies the instance default size", () => {
    expect(sizingBody({ size: "", replicas: "", maxReplicas: "" }, presets)).toEqual({});
  });
  it("resolves a preset to resources, skipping blank quantities", () => {
    expect(sizingBody({ size: "small", replicas: "", maxReplicas: "" }, presets)).toEqual({
      resources: { requests: { cpu: "100m", memory: "128Mi" }, limits: { memory: "512Mi" } },
    });
  });
  it("sends an empty resources block for none", () => {
    expect(sizingBody({ size: "none", replicas: "", maxReplicas: "" }, presets)).toEqual({ resources: {} });
  });
  it("raises max to replicas when replicas exceed the default max", () => {
    expect(sizingBody({ size: "", replicas: "8", maxReplicas: "" }, presets)).toEqual({ scale: { min: 8, max: 8 } });
    expect(sizingBody({ size: "", replicas: "2", maxReplicas: "" }, presets)).toEqual({ scale: { min: 2 } });
  });
  it("rejects max below replicas", () => {
    expect(sizingBody({ size: "", replicas: "3", maxReplicas: "2" }, presets)).toHaveProperty("error");
  });
});
