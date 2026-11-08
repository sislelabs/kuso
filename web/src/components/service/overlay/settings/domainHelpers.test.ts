import { describe, expect, it } from "vitest";
import {
  hostError,
  normalizeHostInput,
  recentAdds,
  recordName,
  registrableDomain,
  causeOf,
  diagnose,
  resolveCheck,
  suggestRecords,
  summarizeDomain,
  type DomainStatus,
} from "./domainHelpers";

describe("host input", () => {
  it("strips scheme, path, port and trailing dot", () => {
    expect(normalizeHostInput(" https://App.Example.com:443/login?x=1 ")).toBe("app.example.com");
    expect(normalizeHostInput("example.com.")).toBe("example.com");
  });
  it("rejects non-hostnames", () => {
    expect(hostError("app.example.com")).toBeNull();
    expect(hostError("*.example.com")).toBeNull();
    expect(hostError("localhost")).not.toBeNull();
    expect(hostError("bad_host.example.com")).not.toBeNull();
  });
});

describe("DNS records", () => {
  it("names records relative to the registered domain", () => {
    expect(registrableDomain("api.shop.example.co.uk")).toBe("example.co.uk");
    expect(recordName("api.shop.example.co.uk")).toBe("api.shop");
    expect(recordName("example.com")).toBe("@");
  });
  it("uses a CNAME for subdomains when the cluster has a hostname, A records otherwise", () => {
    const targets = { ips: ["203.0.113.7", "2001:db8::1"], hostnames: ["lb.example.net"] };
    expect(suggestRecords("app.example.com", targets)).toEqual([{ type: "CNAME", name: "app", value: "lb.example.net" }]);
    expect(suggestRecords("example.com", targets)).toEqual([
      { type: "A", name: "@", value: "203.0.113.7" },
      { type: "AAAA", name: "@", value: "2001:db8::1" },
    ]);
  });
});

describe("summarizeDomain", () => {
  const d = (dns: DomainStatus["dns"]["status"], tls: DomainStatus["tls"]["status"]): DomainStatus => ({
    host: "app.example.com",
    kind: "custom",
    dns: { status: dns, resolved: dns === "mismatch" ? ["198.51.100.1"] : [] },
    tls: { status: tls },
  });
  it("puts DNS problems ahead of certificate state", () => {
    expect(summarizeDomain(d("mismatch", "ready"))).toMatchObject({ tone: "error", pending: true });
    expect(summarizeDomain(d("unresolved", "missing"))).toMatchObject({ label: "Waiting for DNS", pending: true });
  });
  it("is valid only once DNS points here and the certificate is ready", () => {
    expect(summarizeDomain(d("ok", "issuing"))).toMatchObject({ tone: "pending", pending: true });
    expect(summarizeDomain(d("ok", "ready"))).toMatchObject({ tone: "ok", pending: false });
    expect(summarizeDomain(d("proxied", "ready")).label).toContain("Cloudflare");
  });
});

describe("certificate diagnosis", () => {
  const base: DomainStatus = {
    host: "app.example.com",
    kind: "custom",
    dns: { status: "ok", resolved: ["203.0.113.10"] },
    tls: { status: "issuing" },
  };
  const with_ = (dns: Partial<DomainStatus["dns"]>, tls: Partial<DomainStatus["tls"]>): DomainStatus => ({
    ...base,
    dns: { ...base.dns, ...dns },
    tls: { ...base.tls, ...tls },
  });

  it("trusts the server's cause when it sends one", () => {
    expect(causeOf(with_({ status: "mismatch" }, { cause: "rate_limited" }))).toBe("rate_limited");
  });
  it("derives the same cause from an older server's fields", () => {
    expect(causeOf(with_({}, { status: "ready" }))).toBeNull();
    expect(causeOf(with_({ status: "mismatch", resolved: ["198.51.100.1"] }, {}))).toBe("dns_not_pointed");
    expect(causeOf(with_({ status: "unresolved", resolved: [] }, { status: "missing" }))).toBe("dns_not_pointed");
    expect(
      causeOf(with_({}, { status: "failed", reason: "Failed", message: "429 urn:ietf:params:acme:error:rateLimited" })),
    ).toBe("rate_limited");
    expect(causeOf(with_({}, { status: "failed", message: "CAA record for example.com prevents issuance" }))).toBe("caa");
    expect(causeOf(with_({ status: "proxied" }, { status: "failed" }))).toBe("cloudflare_proxy");
    expect(causeOf(with_({}, { status: "failed", reason: "Expired" }))).toBe("expired");
    expect(causeOf(with_({}, {}))).toBe("issuing");
    expect(causeOf(with_({ status: "skipped", resolved: [] }, { status: "missing" }))).toBe("not_requested");
  });
  it("says how long issuance has been running once it's slow", () => {
    const now = Date.parse("2026-11-08T12:00:00Z");
    expect(diagnose(with_({}, { since: "2026-11-08T11:59:00Z" }), now)?.body).toContain("Usually under 2 minutes");
    expect(diagnose(with_({}, { since: "2026-11-08T11:48:00Z" }), now)?.body).toContain("12 minutes");
  });
  it("names the registered domain for CAA and rate limits", () => {
    const caa = diagnose(with_({}, { status: "failed", cause: "caa" }));
    expect(caa?.title).toContain("CAA");
    expect(caa?.body).toContain("example.com");
    expect(caa?.record).toEqual({ type: "CAA", name: "@", value: '0 issue "letsencrypt.org"' });
  });
  it("labels a cluster without cert-manager as HTTP only and stops nagging", () => {
    expect(summarizeDomain(with_({}, { status: "missing", cause: "cert_manager_missing" }))).toMatchObject({
      label: "HTTP only",
      pending: false,
    });
  });
  it("reports the live lookup against the expected addresses", () => {
    expect(resolveCheck(with_({ status: "mismatch", resolved: ["198.51.100.1"] }, {}), ["203.0.113.10"])).toEqual({
      state: "elsewhere",
      resolved: ["198.51.100.1"],
      expected: ["203.0.113.10"],
    });
    expect(resolveCheck(with_({ status: "skipped", resolved: [] }, {}), [])).toBeNull();
  });
});

describe("recentAdds", () => {
  it("keeps only adds inside the window", () => {
    const now = 10 * 60 * 60 * 1000;
    expect(recentAdds([now - 61 * 60 * 1000, now - 5 * 60 * 1000, now], now)).toHaveLength(2);
  });
});
