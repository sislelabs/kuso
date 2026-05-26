# Wildcard TLS via DNS-01 (gap 5)

## Status

**Deferred to next session.** Requires DNS provider credentials plumbing (Cloudflare token at minimum), a new Secret type, and cert-manager `ClusterIssuer` reconciliation per project. Not a YOLO.

## What it solves

Today every per-host cert is issued via HTTP-01: one cert per hostname, one rate-limit slot per cert. Tickero will have `tickero.bg` + 5+ subdomains → 6+ Let's Encrypt orders, 6+ rate-limit slots, 6+ cert-renewal jobs to keep healthy.

A single `*.tickero.bg` wildcard via DNS-01:
- 1 cert covers every subdomain (current + future, no reorder on new service).
- LE rate limit consumed: 1.
- DNS-01 works for hosts behind Cloudflare's proxy too (HTTP-01 doesn't if the proxy strips the `.well-known` path).

## Architecture

```yaml
# KusoProject
spec:
  tls:
    wildcard: true
    dnsProvider: cloudflare
    dnsProviderSecretRef:
      name: tickero-cloudflare
      key: api-token
```

On reconcile, the kuso server:
1. Creates a cert-manager `Issuer` named `<project>-dns01` scoped to the project namespace.
2. Issues a single Certificate for `*.<project.spec.baseDomain>` storing into a Secret named `<project>-wildcard-tls`.
3. Patches every `KusoEnvironment` in the project to set `spec.wildcardCertSecret: <project>-wildcard-tls`, which the env chart's ingress template uses INSTEAD OF the per-host `secretName: <env>-tls`.

## Why deferred

1. **New Secret type**: `dnsProviderSecretRef` introduces a new tenant-writable secret shape that doesn't fit the `<addon>-conn` admission pattern. Either we relax that pattern (security review needed) or add a new whitelisted prefix (`<project>-dns-conn`).
2. **cert-manager DNS-01 webhook installation**: requires cert-manager ≥ 1.10 with the appropriate DNS solver. The kuso install script doesn't currently provision this.
3. **Multi-provider support**: Cloudflare is one. Route53, DigitalOcean, Hetzner DNS each need their own solver config. The schema needs to be provider-tagged from day one or migration is painful.
4. **Wildcard ↔ per-host coexistence**: services with custom non-wildcard domains (`tickero-help.example.com` while the project is `tickero.bg`) still need per-host certs. The ingress template needs to merge both paths.

## Path to ship

1. Pick Cloudflare as the v1 provider (most common, well-documented webhook).
2. Add the schema with `dnsProvider: cloudflare` only (enum-constrained).
3. Plumb the install script to deploy the cert-manager-webhook-cloudflare chart on `kuso install` runs that opt in.
4. Reconciler in `server-go/internal/projects/`: ensure-Issuer + ensure-Certificate per project.
5. Env-ingress chart: prefer `wildcardCertSecret` when present, fall back to per-host.

## Workaround until shipped

User pre-issues a wildcard cert externally (acme.sh, certbot in manual mode), creates a Secret in the namespace, and edits each `KusoEnvironment`'s ingress annotation manually to point at it. Possible. Annoying. The exact pain this gap is meant to remove.
