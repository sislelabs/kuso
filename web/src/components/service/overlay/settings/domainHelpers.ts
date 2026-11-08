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
  type: "A" | "AAAA" | "CNAME" | "CAA";
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
export type TlsCause =
  | "cert_manager_missing"
  | "rate_limited"
  | "caa"
  | "dns_not_pointed"
  | "cloudflare_proxy"
  | "expired"
  | "validation_failed"
  | "issuing"
  | "not_requested";

export interface DomainStatus {
  host: string;
  kind: DomainKind;
  dns: { status: DnsState; resolved: string[]; message?: string };
  tls: {
    status: TlsState;
    notAfter?: string;
    reason?: string;
    message?: string;
    // Newer servers only; older ones omit these and causeOf derives one.
    cause?: TlsCause;
    since?: string;
    lastFailure?: string;
    attempts?: number;
  };
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
  const cause = causeOf(d);
  if (cause === "cert_manager_missing") {
    return { tone: "neutral", label: "HTTP only", detail: "cert-manager isn't installed.", pending: false };
  }
  if (cause === "rate_limited") return { tone: "error", label: "Rate limited", pending: true };
  if (cause === "caa") return { tone: "error", label: "Blocked by CAA", pending: true };
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

// ----- diagnosis: why isn't the certificate serving? -----

function mentions(text: string, ...subs: string[]): boolean {
  const t = text.toLowerCase();
  return subs.some((s) => t.includes(s));
}

// causeOf returns the server's diagnosis, or derives the same one from
// the fields every server version sends.
export function causeOf(d: DomainStatus): TlsCause | null {
  if (d.tls.cause) return d.tls.cause;
  const { status } = d.tls;
  if (status === "ready" || status === "external" || status === "unknown") return null;
  const text = [d.tls.reason, d.tls.message].filter(Boolean).join(" ");
  if (mentions(text, "ratelimited", "rate limit", "too many certificates", "too many failed authorizations")) {
    return "rate_limited";
  }
  if (mentions(text, "error:caa", "caa record")) return "caa";
  if (d.dns.status === "mismatch" || d.dns.status === "unresolved") return "dns_not_pointed";
  if (status === "failed") {
    if (d.tls.reason === "Expired") return "expired";
    return d.dns.status === "proxied" ? "cloudflare_proxy" : "validation_failed";
  }
  return status === "issuing" ? "issuing" : "not_requested";
}

export interface Diagnosis {
  tone: "pending" | "error" | "neutral";
  title: string;
  body: string;
  // Raw cert-manager text, shown secondary when the plain words can't
  // say exactly what went wrong.
  raw?: string;
  // A record that fixes it, shown with copy buttons.
  record?: DnsRecord;
}

const LONG_ISSUE_MS = 5 * 60 * 1000;

export function diagnose(d: DomainStatus, now: number = Date.now()): Diagnosis | null {
  const cause = causeOf(d);
  if (!cause) return null;
  const root = registrableDomain(d.host);
  const raw = [d.tls.reason, d.tls.message].filter(Boolean).join(": ") || undefined;
  switch (cause) {
    case "dns_not_pointed":
      return d.dns.status === "unresolved"
        ? {
            tone: "pending",
            title: "DNS doesn't point here yet",
            body: "There's no DNS record for this name yet. Create the record below; new records usually show up within a few minutes, and the certificate is requested automatically once they do.",
          }
        : {
            tone: "error",
            title: "DNS doesn't point here yet",
            body: "The domain resolves to a different server. Change the record below to the cluster's address; the certificate is requested automatically once it points here.",
          };
    case "caa":
      return {
        tone: "error",
        title: "A CAA record blocks Let's Encrypt",
        body: `${root} has a CAA record that doesn't allow Let's Encrypt. Add this record alongside it, or remove the existing CAA records:`,
        record: { type: "CAA", name: recordName(root), value: '0 issue "letsencrypt.org"' },
        raw,
      };
    case "rate_limited":
      return {
        tone: "error",
        title: "Let's Encrypt rate limit reached",
        body: `Too many certificates or failed attempts for ${root} recently. kuso keeps retrying with growing pauses, so it fixes itself once the limit resets (up to a week for the certificate limit). Don't remove and re-add the domain: that uses up more of the limit.`,
        raw,
      };
    case "cloudflare_proxy":
      return {
        tone: "error",
        title: "Cloudflare's proxy may be blocking the check",
        body: "The domain points at Cloudflare, and Let's Encrypt couldn't reach this cluster through it. Set the record to DNS only until the certificate is issued, or turn off \"Always Use HTTPS\" for this host.",
        raw,
      };
    case "expired":
      return {
        tone: "error",
        title: "Certificate expired",
        body: "Renewal didn't succeed before the certificate ran out. Check that DNS still points here; kuso keeps retrying.",
        raw,
      };
    case "validation_failed":
      return {
        tone: "error",
        title: "Let's Encrypt couldn't verify the domain",
        body: "DNS points here, but the validation request didn't reach this service. kuso retries automatically; if it keeps failing, check for a firewall or proxy in front of the cluster.",
        raw,
      };
    case "cert_manager_missing":
      return {
        tone: "neutral",
        title: "No certificate issuer in this cluster",
        body: "cert-manager isn't installed, so kuso can't request certificates. The domain is served over plain HTTP until it is.",
      };
    case "issuing": {
      const since = d.tls.since ? Date.parse(d.tls.since) : NaN;
      const slow = Number.isFinite(since) && now - since > LONG_ISSUE_MS;
      // Only claim DNS is fine when the server actually looked it up.
      const lead = d.dns.status === "ok" ? "DNS points here and " : "";
      return {
        tone: "pending",
        title: "Still issuing",
        body: slow
          ? `${lead ? `${lead}the` : "The"} certificate has been issuing for ${Math.round((now - since) / 60_000)} minutes, longer than usual. If it isn't done after 15 minutes, the reason shows up here.`
          : `${lead}Let's Encrypt is checking the domain. Usually under 2 minutes.`,
      };
    }
    case "not_requested":
      return {
        tone: "pending",
        title: "Certificate not requested yet",
        body: "kuso requests one as soon as the ingress picks the domain up, usually within a minute of adding it.",
      };
  }
}

export type ResolveState = "match" | "elsewhere" | "unresolved" | "proxied";

export interface ResolveCheck {
  state: ResolveState;
  resolved: string[];
  expected: string[];
}

// resolveCheck is the live "resolves to …" line; null when the server
// didn't look the host up (internal, uncheckable, or ingress IPs unknown).
export function resolveCheck(d: DomainStatus, expected: readonly string[]): ResolveCheck | null {
  const map: Partial<Record<DnsState, ResolveState>> = {
    ok: "match",
    mismatch: "elsewhere",
    unresolved: "unresolved",
    proxied: "proxied",
  };
  const state = map[d.dns.status];
  if (!state) return null;
  return { state, resolved: d.dns.resolved, expected: [...expected] };
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
