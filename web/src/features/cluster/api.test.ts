import { describe, expect, it } from "vitest";
import { normalizeIngressTargets } from "./api";

describe("normalizeIngressTargets", () => {
  it("keeps the IPs and source from a well-formed response", () => {
    expect(
      normalizeIngressTargets({ ips: ["1.2.3.4"], hostnames: [], source: "loadbalancer" }),
    ).toEqual({ ips: ["1.2.3.4"], hostnames: [], source: "loadbalancer" });
  });

  it("treats null arrays and junk entries as empty", () => {
    expect(normalizeIngressTargets({ ips: null, hostnames: ["", 3, "lb.example.com"], source: "nodes" })).toEqual({
      ips: [],
      hostnames: ["lb.example.com"],
      source: "nodes",
    });
  });

  it("reports none when there is nothing to point at", () => {
    expect(normalizeIngressTargets({ ips: [], hostnames: [], source: "nodes" }).source).toBe("none");
    expect(normalizeIngressTargets(undefined).source).toBe("none");
  });
});
