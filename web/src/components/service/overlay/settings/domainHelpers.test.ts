import { describe, expect, it } from "vitest";
import {
  hostError,
  normalizeHostInput,
  recentAdds,
  recordName,
  registrableDomain,
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

describe("recentAdds", () => {
  it("keeps only adds inside the window", () => {
    const now = 10 * 60 * 60 * 1000;
    expect(recentAdds([now - 61 * 60 * 1000, now - 5 * 60 * 1000, now], now)).toHaveLength(2);
  });
});
