import { describe, expect, it } from "vitest";
import { customDomainServing, liveUrlFallback } from "./liveDomain";
import type { KusoEnvironment } from "@/types/projects";

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

describe("liveUrlFallback", () => {
  const env = {
    metadata: { name: "shop-web-production", labels: { "kuso.sislelabs.com/env": "production" } },
    spec: { service: "shop-web", host: "web-shop.kuso.example.com", additionalHosts: ["shop.example.com"] },
  } as unknown as KusoEnvironment;
  it("offers the auto host only when the URL is the custom domain", () => {
    expect(liveUrlFallback(env, "https://shop.example.com")).toEqual({
      customHost: "shop.example.com",
      fallbackUrl: "https://web-shop.kuso.example.com",
      envSegment: "production",
    });
    expect(liveUrlFallback(env, "https://web-shop.kuso.example.com")).toBeUndefined();
    expect(liveUrlFallback(env, undefined)).toBeUndefined();
  });
});
