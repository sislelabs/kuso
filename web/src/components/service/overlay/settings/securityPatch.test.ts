import { describe, expect, it } from "vitest";
import type { KusoService } from "@/types/projects";
import { fromSvc } from "./_primitives";
import { buildServicePatch } from "./buildServicePatch";

const withSC = (securityContext: KusoService["spec"]["securityContext"]) =>
  fromSvc({ metadata: { name: "p-web" }, spec: { securityContext } } as unknown as KusoService);

describe("security patch: user namespace", () => {
  it("reads hostUsers false as the toggle on", () => {
    expect(withSC({ hostUsers: false }).userNamespace).toBe(true);
    expect(withSC({ hostUsers: true }).userNamespace).toBe(false);
    expect(withSC(undefined).userNamespace).toBe(false);
  });

  it("keeps hostUsers when only the capabilities change", () => {
    const base = withSC({ hostUsers: false });
    const res = buildServicePatch({ ...base, capAdd: "SETUID" }, base, {});
    expect("body" in res && res.body.securityContext).toEqual({
      capabilities: { add: ["SETUID"] },
      allowPrivilegeEscalation: false,
      hostUsers: false,
    });
  });

  it("turning the toggle off drops hostUsers", () => {
    const base = withSC({ hostUsers: false });
    const res = buildServicePatch({ ...base, userNamespace: false }, base, {});
    expect("body" in res && res.body.securityContext).toEqual({ allowPrivilegeEscalation: false });
  });
});
