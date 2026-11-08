// Pure helpers for the Domains card: host validation, the DNS records to
// show, how a status payload reads to a person, and the Let's Encrypt
// edit budget. No React here, so domainHelpers.test.ts covers it.

// RFC 1123 hostname, optionally with a leading "*." wildcard label.
const HOSTNAME_RE = /^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{1,62}$/i;

// normalizeHostInput forgives what people paste: a URL with scheme, path
// or port, a trailing dot, upper case.
export function normalizeHostInput(raw: string): string {
  let h = raw.trim().toLowerCase();
  h = h.replace(/^[a-z][a-z0-9+.-]*:\/\//, "");
  h = h.split(/[/?#]/)[0] ?? "";
  h = h.replace(/:\d+$/, "");
  return h.replace(/\.$/, "");
}

export function hostError(host: string): string | null {
  if (!host) return "Enter a domain, e.g. app.example.com";
  if (!HOSTNAME_RE.test(host)) return `"${host}" isn't a valid domain name`;
  return null;
}

// Second-level labels under which registrations happen one level deeper
// (example.co.uk). Not the full public-suffix list: a miss only makes the
// suggested record name longer, never wrong to paste.
const MULTI_LABEL_SUFFIXES = new Set([
  "co.uk", "org.uk", "ac.uk", "gov.uk", "me.uk", "ltd.uk", "plc.uk",
  "com.au", "net.au", "org.au", "edu.au",
  "co.nz", "org.nz", "co.za", "co.jp", "ne.jp", "or.jp",
  "com.br", "com.mx", "com.ar", "com.tr", "com.cn", "com.hk", "com.sg", "com.tw",
  "co.in", "co.kr", "co.il", "com.ua", "com.pl",
]);

export function registrableDomain(host: string): string {
  const labels = host.replace(/^\*\./, "").split(".");
  if (labels.length <= 2) return labels.join(".");
  const lastTwo = labels.slice(-2).join(".");
  if (MULTI_LABEL_SUFFIXES.has(lastTwo) && labels.length >= 3) return labels.slice(-3).join(".");
  return lastTwo;
}

export function isApex(host: string): boolean {
  return registrableDomain(host) === host;
}

// recordName is the "Name"/"Host" column most DNS providers want: the
// part before the registrable domain, "@" for the apex itself.
export function recordName(host: string): string {
  const root = registrableDomain(host);
  if (host === root) return "@";
  return host.slice(0, host.length - root.length - 1);
}

export interface DnsRecord {
  type: "A" | "AAAA" | "CNAME";
  name: string;
  value: string;
}

export interface IngressTargetsLike {
  ips: string[];
  hostnames: string[];
}

// suggestRecords picks the record(s) to create: a CNAME to the load
// balancer hostname for subdomains when there is one (it follows IP
// changes), otherwise A/AAAA records. Apex names can't hold a CNAME.
export function suggestRecords(host: string, targets: IngressTargetsLike): DnsRecord[] {
  const name = recordName(host);
  if (!isApex(host) && targets.hostnames.length > 0) {
    return [{ type: "CNAME", name, value: targets.hostnames[0] }];
  }
  return targets.ips.map((ip) => ({ type: ip.includes(":") ? "AAAA" : "A", name, value: ip }));
}

// ----- status payload (GET .../envs/{env}/domains/status) -----

export type DomainKind = "auto" | "custom" | "wildcard";
export type DnsState = "ok" | "mismatch" | "unresolved" | "proxied" | "skipped";
export type TlsState = "ready" | "issuing" | "failed" | "missing" | "external" | "unknown";

export interface DomainStatus {
  host: string;
  kind: DomainKind;
  dns: { status: DnsState; resolved: string[]; message?: string };
  tls: { status: TlsState; notAfter?: string; reason?: string; message?: string };
}

export interface DomainStatusResponse {
  checkedAt: string;
  expectedIPs: string[];
  domains: DomainStatus[];
}

export type Tone = "ok" | "pending" | "error" | "neutral";

export interface DomainSummary {
  tone: Tone;
  label: string;
  detail?: string;
  // True while the user still has something to do or wait for: keeps
  // the DNS instructions open and the status polling faster.
  pending: boolean;
}

export function summarizeDomain(d: DomainStatus): DomainSummary {
  if (d.dns.status === "mismatch") {
    const at = d.dns.resolved.length ? ` (${d.dns.resolved.join(", ")})` : "";
    return {
      tone: "error",
      label: "Points elsewhere",
      detail: `DNS resolves to another server${at}. Update the record below.`,
      pending: true,
    };
  }
  if (d.dns.status === "unresolved") {
    return {
      tone: "pending",
      label: "Waiting for DNS",
      detail: "No DNS record found yet. Create the record below; changes can take a few minutes to propagate.",
      pending: true,
    };
  }
  switch (d.tls.status) {
    case "failed":
      return {
        tone: "error",
        label: "Certificate failed",
        detail: [d.tls.reason, d.tls.message].filter(Boolean).join(": ") || "cert-manager couldn't issue a certificate.",
        pending: true,
      };
    case "issuing":
      return {
        tone: "pending",
        label: "Issuing certificate",
        detail: "DNS is pointed. Let's Encrypt usually issues within a minute.",
        pending: true,
      };
    case "missing":
      return {
        tone: "pending",
        label: "Waiting for certificate",
        detail: "No certificate requested yet. It starts once the ingress picks the domain up.",
        pending: true,
      };
    case "ready":
      return {
        tone: "ok",
        label: d.dns.status === "proxied" ? "Valid (behind Cloudflare)" : "Valid configuration",
        pending: false,
      };
    case "external":
      return { tone: "ok", label: "Your certificate", detail: "Served with the TLS secret you provided.", pending: false };
    default:
      return { tone: "neutral", label: "Status unknown", detail: d.tls.message, pending: false };
  }
}

// ----- Let's Encrypt edit budget -----
//
// LE allows 5 failed validations per host per hour and 50 certificates per
// registered domain per week (docs/EDIT_SAFETY.md). We can't see LE's
// counters, so count this browser's recent adds per registered domain and
// warn before the user churns into the limit.

export const BUDGET_WINDOW_MS = 60 * 60 * 1000;
export const BUDGET_WARN_AT = 3;

export function recentAdds(timestamps: readonly number[], now: number, windowMs = BUDGET_WINDOW_MS): number[] {
  return timestamps.filter((t) => now - t >= 0 && now - t < windowMs);
}
