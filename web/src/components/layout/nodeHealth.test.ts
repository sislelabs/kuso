import { describe, expect, it } from "vitest";
import { nodePressures, roleLabels } from "./nodeHealth";

describe("nodePressures", () => {
  it("names only the pressure conditions that are True", () => {
    const p = nodePressures({
      conditions: [
        { type: "Ready", status: "True" },
        { type: "DiskPressure", status: "True", message: "ephemeral-storage low" },
        { type: "MemoryPressure", status: "False" },
      ],
    });
    expect(p.map((x) => x.label)).toEqual(["Low on disk"]);
    expect(p[0].message).toBe("ephemeral-storage low");
  });

  it("is empty when the server sends no conditions", () => {
    expect(nodePressures({})).toEqual([]);
  });
});

describe("roleLabels", () => {
  it("shows control plane once for a k3s server", () => {
    expect(roleLabels(["control-plane", "master", "etcd"])).toEqual(["Control plane", "Etcd"]);
  });
});
