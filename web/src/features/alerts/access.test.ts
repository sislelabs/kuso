import { describe, expect, it } from "vitest";
import {
  alertKindsFor,
  alertRequestText,
  canManageAlertRule,
  editableAlertProjects,
  type AlertAccessSession,
} from "./access";
import { buildCreateBody, emptyRuleForm } from "./form";

const member: AlertAccessSession = { adminAll: false, projectRoles: { mine: "editor", own: "admin", peek: "viewer" } };
const admin: AlertAccessSession = { adminAll: true, projectRoles: {} };

describe("canManageAlertRule", () => {
  it("lets a member manage rules on projects they edit only", () => {
    expect(canManageAlertRule(member, { project: "mine", kind: "http_5xx_rate" })).toBe(true);
    expect(canManageAlertRule(member, { project: "own", kind: "cert_expiry" })).toBe(true);
    expect(canManageAlertRule(member, { project: "peek", kind: "http_5xx_rate" })).toBe(false);
    expect(canManageAlertRule(member, { project: "theirs", kind: "http_5xx_rate" })).toBe(false);
  });

  it("keeps cluster-wide and node rules admin-only", () => {
    expect(canManageAlertRule(member, { kind: "dns_mismatch" })).toBe(false);
    expect(canManageAlertRule(member, { project: "mine", kind: "node_cpu" })).toBe(false);
    expect(canManageAlertRule(admin, { kind: "node_cpu" })).toBe(true);
  });
});

describe("pickers", () => {
  it("offers members only the projects they edit, sorted", () => {
    expect(editableAlertProjects(member, ["peek", "mine", "theirs", "own"])).toEqual(["mine", "own"]);
    expect(editableAlertProjects(admin, ["b", "a"])).toEqual(["a", "b"]);
  });

  it("hides node kinds from members", () => {
    expect(alertKindsFor(member)).not.toContain("node_cpu");
    expect(alertKindsFor(admin)).toContain("node_disk");
  });
});

describe("buildCreateBody projectRequired", () => {
  it("rejects an unscoped rule for members, accepts it for admins", () => {
    const f = { ...emptyRuleForm("http_5xx_rate"), name: "x" };
    expect(buildCreateBody(f, { projectRequired: true })).toEqual({ ok: false, error: "Pick a project" });
    expect(buildCreateBody(f).ok).toBe(true);
    expect(buildCreateBody({ ...f, project: "mine" }, { projectRequired: true }).ok).toBe(true);
  });

  it("rejects node kinds for members", () => {
    const f = { ...emptyRuleForm("node_cpu"), name: "x" };
    expect(buildCreateBody(f, { projectRequired: true }).ok).toBe(false);
  });
});

describe("alertRequestText", () => {
  it("names the project, service and kind", () => {
    const t = alertRequestText({ project: "shop", service: "api", kind: "http_p95_latency" });
    expect(t).toContain("shop/api");
    expect(t).toContain("HTTP p95 latency");
  });
});
