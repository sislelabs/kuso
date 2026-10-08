// Suffixes the kusoenvironment chart never requests a certificate for.
// Mirrors server-go/internal/kube/env_scheme.go.
const RESERVED_TLDS = new Set(["local", "internal", "localhost", "test", "example", "invalid", "arpa"]);

// hostScheme returns the scheme a service host is actually served on.
// tlsEnabled is the environment's flag; a reserved-TLD host is plain HTTP
// regardless, because no cert is ever issued for it.
export function hostScheme(tlsEnabled: boolean, host: string): "https" | "http" {
  if (!tlsEnabled) return "http";
  const tld = host.trim().toLowerCase().replace(/\.$/, "").split(".").pop() ?? "";
  return RESERVED_TLDS.has(tld) ? "http" : "https";
}
