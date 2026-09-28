import { describe, expect, it } from "vitest";
import { buildCreateBody, describeRule, emptyRuleForm, parseDur, type RuleFormState } from "./form";
import type { AlertRule } from "./api";

function form(over: Partial<RuleFormState>): RuleFormState {
  return { ...emptyRuleForm("http_5xx_rate"), ...over };
}

describe("buildCreateBody", () => {
  it("maps a 5xx rule: percent → thresholdFloat, min requests → thresholdInt, env scope kept", () => {
    const r = buildCreateBody(
      form({ name: " 5xx ", project: "shop", service: "web", env: "production", threshold: "5", minRequests: "50" })
    );
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.body).toMatchObject({
      name: "5xx",
      kind: "http_5xx_rate",
      project: "shop",
      service: "web",
      env: "production",
      thresholdFloat: 5,
      thresholdInt: 50,
      windowSeconds: 300,
      throttleSeconds: 600,
    });
  });

  it("maps cert_expiry days to thresholdInt and never sends a float", () => {
    const r = buildCreateBody(form({ kind: "cert_expiry", name: "certs", threshold: "21" }));
    expect(r.ok && r.body.thresholdInt).toBe(21);
    expect(r.ok && r.body.thresholdFloat).toBeUndefined();
  });

  it("sends no threshold for dns_mismatch and omits empty scope", () => {
    const r = buildCreateBody(form({ kind: "dns_mismatch", name: "dns", threshold: "", minRequests: "" }));
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.body.thresholdInt).toBeUndefined();
    expect(r.body.thresholdFloat).toBeUndefined();
    expect(r.body.project).toBeUndefined();
  });

  it("rejects service/env without a project", () => {
    const r = buildCreateBody(form({ name: "x", service: "web" }));
    expect(r.ok).toBe(false);
  });

  it("rejects a non-numeric threshold", () => {
    const r = buildCreateBody(form({ name: "x", threshold: "lots" }));
    expect(r.ok).toBe(false);
  });

  it("drops env for node rules", () => {
    const r = buildCreateBody(form({ kind: "node_cpu", name: "cpu", env: "production", threshold: "90" }));
    expect(r.ok && r.body.env).toBeUndefined();
    expect(r.ok && r.body.thresholdFloat).toBe(90);
  });

  it("requires a query for log_match", () => {
    expect(buildCreateBody(form({ kind: "log_match", name: "oom", threshold: "1" })).ok).toBe(false);
  });
});

describe("describeRule", () => {
  const base: AlertRule = {
    id: "r",
    name: "n",
    enabled: true,
    kind: "http_p95_latency",
    windowSeconds: 600,
    severity: "warn",
    throttleSeconds: 600,
    createdAt: "",
    updatedAt: "",
  };
  it("renders each kind in its unit", () => {
    expect(describeRule({ ...base, thresholdFloat: 800, thresholdInt: 20 })).toBe("p95 ≥ 800ms over 10m (min 20 req)");
    expect(describeRule({ ...base, kind: "cert_expiry", thresholdInt: 14 })).toBe("expires within 14d or not Ready");
    expect(describeRule({ ...base, kind: "dns_mismatch" })).toBe("host doesn't resolve to the cluster");
  });
});

describe("parseDur", () => {
  it("parses units", () => {
    expect(parseDur("5m")).toBe(300);
    expect(parseDur("90")).toBe(90);
    expect(parseDur("1h")).toBe(3600);
    expect(parseDur("nope")).toBe(0);
  });
});
