import { describe, expect, it } from "vitest";
import type { KusoService } from "@/types/projects";
import { fromSvc } from "./_primitives";

describe("fromSvc volumes", () => {
  it("keeps storageClass and accessMode so a save doesn't rewrite immutable PVC fields", () => {
    const svc = {
      metadata: { name: "alpha-web" },
      spec: {
        project: "alpha",
        volumes: [
          { name: "data", mountPath: "/data", sizeGi: 5, storageClass: "longhorn", accessMode: "ReadWriteMany" },
          { name: "tmp", mountPath: "/tmp" },
        ],
      },
    } as unknown as KusoService;
    expect(fromSvc(svc).volumes).toEqual([
      { name: "data", mountPath: "/data", sizeGi: 5, storageClass: "longhorn", accessMode: "ReadWriteMany" },
      { name: "tmp", mountPath: "/tmp", sizeGi: 1 },
    ]);
  });
});
