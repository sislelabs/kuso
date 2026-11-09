import { describe, expect, it } from "vitest";
import {
  autoRuleName,
  buildCreateBody,
  describeRule,
  emptyRuleForm,
  joinDur,
  parseDur,
  prefillRuleForm,
  ruleStatus,
  splitDur,
  type RuleFormState,
} from "./form";
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
    expect(describeRule({ ...base, thresholdFloat: 800, thresholdInt: 20 })).toBe("p95 ≥ 800 ms over 10m (min 20 req)");
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

describe("prefillRuleForm", () => {
  const p = (q: string) => prefillRuleForm(new URLSearchParams(q));

  it("prefills a log_match query", () => {
    expect(p("new=1&kind=log_match&project=shop&service=worker&query=failed%20to%20send")).toMatchObject({
      kind: "log_match",
      service: "worker",
      query: "failed to send",
    });
  });

  it("returns null without new=1", () => {
    expect(p("project=a&kind=http_5xx_rate")).toBeNull();
  });

  it("prefills kind + scope + a name", () => {
    const f = p("new=1&project=demo&service=api&env=production&kind=http_p95_latency");
    expect(f).toMatchObject({ kind: "http_p95_latency", project: "demo", service: "api", env: "production", name: "api HTTP p95 latency" });
  });

  it("maps the error_rate alias and ignores unknown kinds", () => {
    expect(p("new=1&kind=error_rate")?.kind).toBe("http_5xx_rate");
    expect(p("new=1&kind=bogus")?.kind).toBe("http_5xx_rate");
  });

  it("drops scope the kind can't use, and service without project", () => {
    expect(p("new=1&kind=node_cpu&project=demo&service=api")).toMatchObject({ project: "", service: "", name: "Node CPU" });
    expect(p("new=1&kind=log_match&project=demo&env=prod")).toMatchObject({ project: "demo", env: "" });
    expect(p("new=1&service=api")).toMatchObject({ project: "", service: "" });
  });
});

describe("new kinds and form helpers", () => {
  it("names a rule from its kind and scope until the user types one", () => {
    expect(emptyRuleForm("crashloop").name).toBe("Crash loop");
    expect(autoRuleName({ kind: "deploy_failed", project: "shop", service: "" })).toBe("shop Deploy failed");
    expect(autoRuleName({ kind: "healthcheck_failing", project: "shop", service: "web" })).toBe("web Uptime check failing");
  });

  it("stores crashloop restarts and healthcheck minutes as thresholdInt", () => {
    const c = buildCreateBody(form({ kind: "crashloop", name: "c", threshold: "4", minRequests: "" }));
    expect(c.ok && c.body).toMatchObject({ kind: "crashloop", thresholdInt: 4 });
    const h = buildCreateBody(form({ kind: "healthcheck_failing", name: "h", threshold: "10", minRequests: "" }));
    expect(h.ok && h.body).toMatchObject({ kind: "healthcheck_failing", thresholdInt: 10, windowSeconds: undefined });
  });

  it("splits and joins durations", () => {
    expect(splitDur("5m")).toEqual({ n: "5", unit: "m" });
    expect(splitDur("300")).toEqual({ n: "300", unit: "s" });
    expect(parseDur(joinDur("2", "h"))).toBe(7200);
  });

  it("tells firing, ok, never fired and disabled apart", () => {
    const now = Date.parse("2026-11-09T12:00:00Z");
    const rule = (o: Partial<AlertRule>): AlertRule => ({
      id: "r", name: "r", enabled: true, kind: "http_5xx_rate", windowSeconds: 300, severity: "warn", throttleSeconds: 600, createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z", ...o,
    });
    expect(ruleStatus(rule({ enabled: false, firingSince: "2026-11-09T11:00:00Z" }), now)).toBe("disabled");
    expect(ruleStatus(rule({ firingSince: "2026-11-09T11:00:00Z" }), now)).toBe("firing");
    expect(ruleStatus(rule({}), now)).toBe("never");
    expect(ruleStatus(rule({ lastFiredAt: "2026-11-09T11:59:00Z" }), now)).toBe("ok");
    expect(ruleStatus(rule({ kind: "log_match", lastFiredAt: "2026-11-09T11:59:00Z" }), now)).toBe("firing");
    expect(ruleStatus(rule({ kind: "log_match", lastFiredAt: "2026-11-08T11:59:00Z" }), now)).toBe("ok");
  });
});
