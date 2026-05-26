# kuso readiness gaps — Tickero impact ranking

That's most of the platform. The pieces below are the real delta.

---

## The actual gaps (ranked by Tickero impact)

### 🔴 P0 — would block or risk Tickero in prod

#### 1. No "release phase" → migrations bake into entrypoint

Today you'd put `migrate up && exec /app/api` in the API's CMD. That works but has two real failure modes:

- Two API replicas race to apply the same migration on rollout.
- A migration takes 90s; pod fails its readiness probe; deploy thrashes.

**Add to kuso:** a `KusoService.spec.release.command` (or `releaseHook`) that the operator runs as a KusoRun-style Job before the new image scales up. Heroku-style. ~150 lines in the operator reconciler + a doc page. This is the single highest-leverage addition for any app with a real DB.

#### 2. Payment-callback path can't sleep

ePay.bg `URL_NOTIFY` POSTs to `/api/v1/payments/notify`. If the API has `sleep.afterMinutes` on, the cold start can exceed ePay's retry timeout and you get duplicate/late confirmations. Today kuso has sleep as a service-level toggle.

**Add to kuso:** per-path `wakeOn` exclusion list, or simpler — a `sleep: disabled` flag that the docs strongly recommend for any service handling third-party callbacks. Even a docs section ("Sleep is great for backoffice and preview envs; turn it off for anything that receives webhooks/callbacks") would help. ~20 lines + an EDIT_SAFETY.md row.

#### 3. NATS JetStream isn't HA

You already have first-class NATS, but `ha: true` is silently ignored. JetStream is your queue for emails, PDFs, payouts, refunds — losing the NATS pod loses jobs in flight until a restart picks up redelivery. For a payment platform, this is the wrong story.

**Add to kuso:** the `nats-ha.yaml` template (3-replica clustered JetStream with `--cluster` + `--routes` + headless service). Mirror the existing Redis-HA pattern. Research agent estimates ~180 lines.

### 🟡 P1 — operationally important, workable manually

#### 4. Addon backups are DIY scripts

Control-plane Postgres has `kuso backup`. App Postgres (orders, tickets, money), Redis (seat holds), MinIO (event images) don't. The doc says "use kuso addon backup" but that's per-addon and one-shot. For a payment platform you want daily snapshots to off-cluster storage with retention.

**Add to kuso:** `KusoAddon.spec.backup: { schedule, retention, destination }` that generates a CronJob per addon — `pg_dump`/`BGSAVE`/`mc mirror` into the addon's S3 (or an external bucket). This unlocks honest disaster recovery; without it, every kuso install reinvents this badly.

#### 5. Wildcard cert is per-host today

You'll have `tickero.bg` + at least 5 subdomains. With per-host LE issuance that's 6 certs and 6 LE rate-limit risks. You can pre-issue a `*.tickero.bg` and mount it, but the path through the platform is "edit the Secret name manually."

**Add to kuso:** `KusoProject.spec.tls.wildcard: true` → cert-manager issues a single `*.<baseDomain>` cert via DNS-01 (needs DNS provider creds — Cloudflare token is the common one) and the operator points every per-host TLS reference at that single secret. ~250 lines + a DNS-creds Secret type. Bigger lift but huge ergonomic win for any app with N subdomains.

#### 6. No app-log streaming / aggregation

You currently use Loki+Grafana. Kuso's logs live in control-plane Postgres, 14-day, no streaming. For day-to-day debugging that's fine; for incident response on a payment outage at 11pm, you want tail/grep over weeks.

**Add to kuso (optional):** a "log sink" addon — `KusoAddon kind=loki` + `KusoAddon kind=promtail-daemonset` that just installs the stack on the cluster, exposes a Grafana addon, and writes the standard datasources. You're already running this on Tickero today; making it a first-class kuso addon means you keep your observability story when you migrate, and every kuso user gets it. Honestly this might be the single most differentiating addition.

### 🟢 P2 — nice to have

#### 7. No production mailer

Only Mailpit. Tickero will want Resend or SES in prod.

**Add to kuso:** `KusoAddon kind=mail-external` that takes `provider: resend|ses|smtp` + creds and just synthesizes a conn-secret with `SMTP_HOST/PORT/USER/PASS` or `RESEND_API_KEY`. ~80 lines. Saves every user the "wire it manually via spec.external" dance.

#### 8. Rollback isn't one-click

Today: edit `image.tag` back manually. For a 2am page, that's friction.

**Add to kuso:** `kuso rollback <service>` CLI / button — fetches the previous successful KusoBuild, repins. ~50 lines in the server + 1 CLI command.

#### 9. No webhook on cron failure

KusoCron runs Tickero's scheduled jobs (reminders, refund deadlines, voucher expiry). If they fail silently, you find out from angry users.

**Add to kuso:** `KusoCron.spec.onFailure: { webhookURL, secretRef }` that POSTs the job status. ~60 lines.

---

## What you should NOT add to kuso

A few things I'd resist scope-creeping into:

- **External secret managers** — Vault/AWS Secrets Manager via the existing external-secrets operator pattern; don't reinvent it inside kuso.
- **Multi-region failover** — explicitly out per the README, and rightly so. Cloudflare + managed Postgres handle this.
- **Velero-style cluster backup** — overlap with addon backups; pick one (I'd pick addon backups).

---

## What I'd actually build first

If you ship just three additions to kuso, in this order, Tickero becomes a clean fit and every serious app on kuso benefits:

1. **NATS HA template** (1–2 days) — closes the only "your queue can lose jobs" gap. Mechanical work, low risk.
2. **`KusoService.spec.release.command` release hook** (3–5 days) — closes the migration-on-deploy footgun. Most-asked-about thing for any app with a DB.
3. **`KusoAddon.spec.backup` with cron + retention** (1 week) — turns kuso from "self-hosted toy" into "I'd trust this with payment data." Biggest credibility upgrade per LOC.
