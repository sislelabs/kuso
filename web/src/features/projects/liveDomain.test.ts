import { describe, expect, it } from "vitest";
import { customDomainServing } from "./liveDomain";

const d = (dns: string, tls: string) => ({ host: "shop.example.com", dns: { status: dns }, tls: { status: tls } });

describe("customDomainServing", () => {
  it("drops a custom domain whose DNS or cert isn't ready", () => {
    expect(customDomainServing(d("unresolved", "missing"))).toBe(false);
    expect(customDomainServing(d("mismatch", "ready"))).toBe(false);
    expect(customDomainServing(d("ok", "issuing"))).toBe(false);
  });
  it("keeps a serving or unknown domain", () => {
    expect(customDomainServing(d("ok", "ready"))).toBe(true);
    expect(customDomainServing(d("proxied", "external"))).toBe(true);
    expect(customDomainServing(undefined)).toBe(true);
  });
});
