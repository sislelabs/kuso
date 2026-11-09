import { describe, expect, it } from "vitest";
import { formatCPU, formatMemory, parseCPU, parseMemory, scaleImpact } from "./scaleImpact";

describe("scaleImpact", () => {
  it("parses kubernetes quantities", () => {
    expect(parseCPU("100m")).toBe(0.1);
    expect(parseCPU("2")).toBe(2);
    expect(parseCPU("")).toBeNull();
    expect(parseMemory("128Mi")).toBe(128 * 1024 ** 2);
    expect(parseMemory("1Gi")).toBe(1024 ** 3);
    expect(parseMemory("500M")).toBe(500e6);
    expect(parseMemory("lots")).toBeNull();
  });

  it("multiplies per-pod requests by the replica bounds", () => {
    const r = scaleImpact({ min: "1", max: "5", cpuRequest: "100m", memRequest: "128Mi", cpuLimit: "", memLimit: "1Gi", environments: 3 });
    expect(r?.cpuRequest).toEqual([0.1, 0.5]);
    expect(r?.memRequest).toEqual([128 * 1024 ** 2, 640 * 1024 ** 2]);
    expect(r?.cpuLimit).toBeNull();
    expect(r?.memLimit).toEqual([1024 ** 3, 5 * 1024 ** 3]);
    expect(r?.environments).toBe(3);
  });

  it("counts a sleeping service as one pod while awake", () => {
    const r = scaleImpact({ min: "0", max: "1", cpuRequest: "250m", memRequest: "", cpuLimit: "", memLimit: "", environments: 1 });
    expect(r?.minPods).toBe(1);
    expect(r?.cpuRequest).toEqual([0.25, 0.25]);
  });

  it("formats totals readably", () => {
    expect(formatCPU(0.5)).toBe("500m");
    expect(formatCPU(1.5)).toBe("1.5");
    expect(formatMemory(640 * 1024 ** 2)).toBe("640Mi");
    expect(formatMemory(5 * 1024 ** 3)).toBe("5Gi");
  });
});
