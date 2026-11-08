// Display-side heuristic for "does this env var look like a secret?".
// It only decides the DEFAULT masking in the editor — storage is the
// server's call (chooseEnvStorage puts almost everything in the managed
// Secret, so "stored as secret" says nothing about sensitivity) and
// revealing still needs secrets:read.

import { buildTimePrefix } from "@/components/service/envVarTransforms";

// Matched against whole `_`-separated segments, so KEYCLOAK_URL and
// PASSPORT_MODE stay readable while API_KEY / DB_PASS do not.
const SENSITIVE_SEGMENTS = new Set([
  "TOKEN",
  "TOKENS",
  "SECRET",
  "SECRETS",
  "KEY",
  "KEYS",
  "APIKEY",
  "PASSWORD",
  "PASSWORDS",
  "PASSWD",
  "PASSPHRASE",
  "PASS",
  "PWD",
  "DSN",
  "CREDENTIAL",
  "CREDENTIALS",
  "CREDS",
  "PRIVATE",
  "AUTH",
  "COOKIE",
  "SALT",
  "PEPPER",
  "SIGNATURE",
  "SIGNING",
]);

// Webhook URLs carry their secret in the path (Slack, Discord), so the
// metadata-tail exception below never applies to them.
const ALWAYS_SENSITIVE_SEGMENTS = new Set(["WEBHOOK", "WEBHOOKS"]);

// A sensitive word followed by one of these names metadata ABOUT the
// secret, not the secret itself: COOKIE_DOMAIN, TOKEN_TTL, AUTH_URL.
// The URL value check below still catches an AUTH_URL with credentials.
const METADATA_TAILS = new Set([
  "URL",
  "URI",
  "HOST",
  "HOSTNAME",
  "ENDPOINT",
  "ISSUER",
  "DOMAIN",
  "PORT",
  "PATH",
  "REGION",
  "ENABLED",
  "DISABLED",
  "TTL",
  "EXPIRY",
  "EXPIRES",
  "MAXAGE",
  "HEADER",
  "NAME",
  "SECURE",
  "SAMESITE",
  "LENGTH",
  "ALGORITHM",
  "ALG",
]);

export function isSensitiveKey(name: string): boolean {
  const upper = name.trim().toUpperCase();
  if (!upper) return false;
  // Framework-public prefixes are inlined into the browser bundle, so
  // they are public by construction (NEXT_PUBLIC_MAPBOX_TOKEN).
  if (buildTimePrefix(upper)) return false;
  const segs = upper.split("_").filter(Boolean);
  const last = segs[segs.length - 1];
  if (segs.some((s) => ALWAYS_SENSITIVE_SEGMENTS.has(s))) return true;
  for (let i = 0; i < segs.length; i++) {
    if (!SENSITIVE_SEGMENTS.has(segs[i])) continue;
    if (i < segs.length - 1 && METADATA_TAILS.has(last)) continue;
    return true;
  }
  return false;
}

// scheme://user:password@host — the credential sits in the value even when
// the key (DATABASE_URL, REDIS_URL, AMQP_URL) looks harmless. A bare
// user@host without a password isn't treated as secret.
const URL_WITH_PASSWORD = /^[a-z][a-z0-9+.-]*:\/\/[^\s/@:]*:[^\s/@]+@/i;

export function valueLooksSecret(value: string): boolean {
  const v = value.trim();
  if (!v) return false;
  if (URL_WITH_PASSWORD.test(v)) return true;
  if (v.includes("-----BEGIN") && v.includes("PRIVATE KEY")) return true;
  return false;
}

export function isSensitiveVar(name: string, value: string): boolean {
  return isSensitiveKey(name) || valueLooksSecret(value);
}
