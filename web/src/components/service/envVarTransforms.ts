// Pure transform logic for the env-vars editor, extracted from
// EnvVarsEditor.tsx so the lossy-round-trip-sensitive conversions
// (secretKeyRef ↔ ${{ addon.KEY }} refs, resolved service DNS ↔
// ${{ svc.URL/HOST }} refs, rows ↔ dotenv text) are unit-testable
// without rendering the component. No React, no state — every export
// is a pure function over plain data. Behavior is a 1:1 move from the
// component; see envVarTransforms.test.ts for the round-trip contract.

import type { KusoEnvVar } from "@/types/projects";

export interface Row {
  // Stable per-row id assigned at row-creation time. React's `key`
  // attribute reads this so renaming the var (which mutates `name`
  // on every keystroke) doesn't unmount the row + steal focus from
  // the input. Generated client-side; never persisted.
  id: string;
  name: string;
  value: string;
  // fromSecret marks a row backed by an OPAQUE secretKeyRef the editor
  // has no user-facing representation for — a manually-mounted secret,
  // a legacy fieldRef/configMapKeyRef, or an addon ref that hasn't
  // resolved yet (addons query still in flight). It stays non-editable:
  // the user re-wires it with the 🔗 picker rather than typing over it.
  // Addon/shared refs that DO resolve are NOT fromSecret — they render
  // as an editable ${{ addon.KEY }} value like any other. This is the
  // "one secret primitive" model: every row is just a value; the server
  // decides storage on save (auto:true). There is no user-facing "store
  // as secret" concept any more.
  fromSecret: boolean;
  visible: boolean;
  // secretBacked marks a row whose real value lives OFF the CR — a
  // managed <service>-secrets value (server tags it source:
  // "managed-secret") or an addon/shared secretKeyRef. It arrives
  // masked/blank on the default read, so the editor renders a "•••••"
  // placeholder and lets the eye trigger a reveal fetch to show the real
  // value. It's purely a display hint — save still routes through the
  // unified {value, auto:true} write, so a secretBacked row the user
  // re-types is stored wherever the server decides.
  secretBacked: boolean;
  // managed is true ONLY for a row loaded as source="managed-secret",
  // i.e. a value that lives in the <service>-secrets Secret with NO
  // spec.envVars entry. It's the one removal case the bulk POST (which
  // overwrites spec.envVars) can't delete — the value survives in the
  // Secret — so removing such a row needs an explicit per-key DELETE.
  // Every other row (literal, opaque secretKeyRef, resolved addon ref)
  // is deleted implicitly by being omitted from the bulk overwrite.
  managed?: boolean;
  // origValueFrom preserves the raw `valueFrom` blob the server sent
  // for a secret-backed row that the editor has NO user-facing
  // representation for (legacy fieldRef / configMapKeyRef, a
  // secretKeyRef against a renamed/deleted addon, or a ref that
  // simply hasn't resolved because the addons query is still in
  // flight). Without re-emitting it verbatim, toEnvVar collapsed these
  // rows to `{name}` only — and the server, seeing a name with neither
  // value nor valueFrom, DROPPED the var on every save. We stash the
  // original here at load time and hand it straight back on save so an
  // untouched fromSecret row round-trips losslessly. Undefined for
  // rows the editor CAN represent (plain values, addon refs) — those
  // re-resolve from their edited value.
  origValueFrom?: Record<string, unknown>;
}

// rid mints a fresh id for a Row. Math.random is fine — these
// only need to be unique within the current editor session.
export function rid(): string {
  return Math.random().toString(36).slice(2, 10);
}

// reservedEnvWarning mirrors server-go's projects.envNameReserved
// rules so the user sees the conflict at typing time, not at save
// time. Server is still authoritative — these are nudges. Returns
// the reason string when the name is reserved; empty string otherwise.
export function reservedEnvWarning(name: string): string {
  if (!name) return "";
  if (name === "PORT") {
    return "PORT is set by kuso from Settings → Networking → Port";
  }
  if (name === "HOSTNAME") {
    return "HOSTNAME is reserved by the kubelet";
  }
  if (name.startsWith("KUBERNETES_")) {
    return "KUBERNETES_* is reserved for in-cluster API access";
  }
  return "";
}

// addonByConnSecret maps "<project>-<addon>-conn" → "<addon>" so the
// editor can detect a secretKeyRef that originally came from an addon
// ref like ${{ postgres.DATABASE_URL }} and render it as a ref again
// instead of an opaque (from secret) row. Without this the round-trip
// — type ref, save, reload — collapses to a disabled placeholder and
// the user can't edit their own value.
export function addonShortByConnSecret(
  addons: ReadonlyArray<{ metadata: { name: string }; status?: { connectionSecret?: string } }>,
  project: string
): Map<string, string> {
  const out = new Map<string, string>();
  const prefix = project + "-";
  for (const a of addons) {
    const fqn = a.metadata?.name ?? "";
    const short = fqn.startsWith(prefix) ? fqn.slice(prefix.length) : fqn;
    const sec = a.status?.connectionSecret;
    if (sec) out.set(sec, short);
    // Fallback: addons without a populated status yet still follow the
    // canonical "<fqn>-conn" naming. Index that too so freshly-created
    // addons round-trip before the operator backfills status.
    if (fqn) out.set(fqn + "-conn", short);
  }
  return out;
}

export function toRow(
  v: KusoEnvVar,
  project: string,
  addonByConn: Map<string, string>,
  knownScopes: string[] = []
): Row {
  // Detect "secretKeyRef pointing at a known addon" and render it as a
  // ${{ <addon>.<KEY> }} ref instead of treating it as opaque. Anything
  // else with a valueFrom (manual secretKeyRef, fieldRef, etc.) stays
  // fromSecret because we have no user-facing representation for it.
  //
  // Managed-secret keys (source: "managed-secret") live in the
  // <service>-secrets mount off the CR. On the default read they carry
  // no value; on a reveal read the server resolves the plaintext into
  // `value`. Under the "one secret primitive" model there's no separate
  // managed-secret row type any more — it's just a value row whose real
  // value is secret-backed (masked until revealed), edited + saved like
  // any other via the unified auto write.
  if (v.source === "managed-secret") {
    return {
      id: rid(),
      name: v.name ?? "",
      value: v.value ?? "", // populated on a reveal read; blank otherwise
      fromSecret: false,
      secretBacked: true,
      managed: true,
      visible: false,
    };
  }
  const ref = addonRefFromValueFrom(v.valueFrom, addonByConn);
  if (ref) {
    // A resolved addon/shared ref. Editable as a ${{ addon.KEY }} value;
    // secret-backed so the eye can reveal the underlying plaintext.
    return { id: rid(), name: v.name ?? "", value: ref, fromSecret: false, secretBacked: true, visible: false };
  }
  const fromSecret = !!v.valueFrom;
  // On a reveal read the server populates `value` even for a secretKeyRef
  // (keeping valueFrom); on the default read it's blank for fromSecret.
  const raw = v.value ?? "";
  return {
    id: rid(),
    name: v.name ?? "",
    // Reverse server-resolved literals back to ${{ x.KEY }} form so
    // the editor shows the original ref the user wrote. fromSecret rows
    // are opaque — the reveal read may populate `value` for the eye, but
    // save re-emits their original valueFrom rather than the plaintext.
    value: fromSecret ? raw : literalToRef(raw, project, knownScopes),
    fromSecret,
    // A valueFrom we couldn't represent is still secret-backed for the
    // eye/reveal affordance; a plain literal is not.
    secretBacked: fromSecret,
    visible: false,
    // Stash the opaque valueFrom so save() can re-emit it unchanged —
    // the editor can't render it, but it must not silently delete it.
    origValueFrom: fromSecret ? v.valueFrom : undefined,
  };
}

// addonRefFromValueFrom picks the secretKeyRef out of a valueFrom blob
// (server returns it as `Record<string, unknown>` to stay forward-compat
// with future kube valueFrom variants) and, if it points at a known
// addon's connection secret, returns the equivalent `${{ <addon>.<KEY> }}`
// ref. Returns "" when the secretKeyRef is opaque (manually-mounted
// secret unrelated to a kuso addon) so the caller falls back to the
// fromSecret display path.
export function addonRefFromValueFrom(
  vf: Record<string, unknown> | undefined,
  addonByConn: Map<string, string>
): string {
  if (!vf) return "";
  const skr = vf.secretKeyRef as { name?: string; key?: string } | undefined;
  if (!skr || !skr.name || !skr.key) return "";
  const short = addonByConn.get(skr.name);
  if (!short) return "";
  return `\${{ ${short}.${skr.key} }}`;
}

// rowDiffLabel renders a Row's value for the confirm-dialog diff without
// leaking the plaintext of a genuine secret. Rules:
//   - opaque secret ref (fromSecret) → "<secret>"
//   - a ${{ ref }} value → shown verbatim (names the source, not the
//     sensitive value; clipped so a long ref doesn't overflow the modal)
//   - a secret-backed value the user retyped → masked ("•••••"), since the
//     server may store it as a managed secret and the diff is shoulder-
//     surfable
//   - a plain literal → the value, clipped to 60 chars
export function rowDiffLabel(r: Row): string {
  if (r.fromSecret) return "<secret>";
  const val = r.value ?? "";
  if (val.includes("${{")) {
    return val.length > 60 ? val.slice(0, 57) + "…" : val;
  }
  if (r.secretBacked) return "•••••";
  if (val.length > 60) return val.slice(0, 57) + "…";
  return val;
}

// Valid POSIX-ish env-var name: starts with a letter or underscore,
// rest letter/digit/underscore. Required because k8s accepts the
// CR but the kubelet drops invalid names from the pod env silently
// — the user types "FOO BAR" and gets nothing on the pod.
export const ENV_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

// ENV_MASK_SENTINEL mirrors the server's envMaskSentinel — the value a
// non-admin (masked) session sees instead of real plaintext. The editor
// is read-only when masked, but cleanRows also refuses to write this
// literal back so it can never clobber a real value (defense in depth).
export const ENV_MASK_SENTINEL = "••••••••";

// literalToRef reverses the server-side service-ref resolution. When
// a value matches the cluster-local DNS shape we recognise, render it
// as the equivalent `${{ <svc>.<KEY> }}` token. The server will
// re-expand on save, so the round-trip is lossless.
//
// Patterns we recognise (URL first to avoid mis-classifying
// "http://host:port" as a HOST):
//   http://<svc-fqn>-<scope>.<ns>.svc.cluster.local:<port> → ${{ svc.URL }}
//   <svc-fqn>-<scope>.<ns>.svc.cluster.local               → ${{ svc.HOST }}
//
// The first DNS label is "<project>-<svc>-<envScope>" (the server
// resolves the ref against a concrete env, stamping its scope). We
// strip BOTH the "<project>-" prefix AND the trailing "-<scope>"
// segment to recover the short service name the user typed. Without
// the scope strip, "http://proj-api-production.kuso.svc.cluster.local"
// reverses to ${{ api-production.URL }} — corrupting the ref on a
// no-op read→save round-trip (the saved value then points at a
// non-existent service "api-production").
//
// knownScopes are the env scopes for this project (production, staging,
// preview-pr-7, ...) so we strip a real scope and never mistake a dash
// in the service name itself for a scope boundary.
export function literalToRef(value: string, project: string, knownScopes: string[] = []): string {
  if (!value) return value;
  const urlMatch = value.match(
    /^http:\/\/([a-z0-9-]+)\.[a-z0-9-]+\.svc\.cluster\.local(?::\d+)?$/
  );
  if (urlMatch) {
    return `\${{ ${shortServiceFromLabel(urlMatch[1], project, knownScopes)}.URL }}`;
  }
  const hostMatch = value.match(
    /^([a-z0-9-]+)\.[a-z0-9-]+\.svc\.cluster\.local$/
  );
  if (hostMatch) {
    return `\${{ ${shortServiceFromLabel(hostMatch[1], project, knownScopes)}.HOST }}`;
  }
  return value;
}

// shortServiceFromLabel recovers the short service name from a resolved
// DNS label "<project>-<svc>-<envScope>". Strips the project prefix, then
// a recognised trailing env-scope segment. Falls back to stripping a
// trailing "-production" when the scope list is unavailable (the dominant
// case — service refs resolve against production at SetEnv time).
export function shortServiceFromLabel(label: string, project: string, knownScopes: string[]): string {
  const name = stripProjectPrefix(label, project);
  const scopes = knownScopes.length ? knownScopes : ["production"];
  for (const scope of scopes) {
    const suffix = "-" + scope;
    // Only strip if there's a real service name left in front of it,
    // so a service literally named after a scope isn't emptied out.
    if (name.endsWith(suffix) && name.length > suffix.length) {
      return name.slice(0, name.length - suffix.length);
    }
  }
  return name;
}

// stripProjectPrefix returns the user-friendly short name from a
// project-prefixed kube name. KusoService CRs are named
// "<project>-<short>"; the editor + canvas display the short form.
export function stripProjectPrefix(fqn: string, project: string): string {
  const prefix = project + "-";
  if (fqn.startsWith(prefix)) return fqn.slice(prefix.length);
  return fqn;
}

// Serialize plain (non-secret) rows to dotenv format. Secret-backed
// rows are emitted as a comment so the user sees them in the bulk
// view but can't accidentally rewrite them as plain values.
export function rowsToDotenv(rows: Row[]): string {
  return rows
    .map((r) => {
      // Opaque secret-ref rows (fromSecret) have no representable value in
      // the dotenv textarea — emit them as a comment so they're visible
      // but can't be rewritten as a plain literal. Secret-backed VALUE
      // rows (managed secrets / addon refs) whose plaintext isn't revealed
      // also can't round-trip through the textarea, so comment them too.
      if (r.fromSecret || (r.secretBacked && r.value === "")) {
        return `# ${r.name}=<from secret>`;
      }
      const v = r.value ?? "";
      // Quote when the value contains whitespace, =, or # so the
      // round-trip parse picks it back up unchanged. Newlines become
      // \n escapes — the parser is line-based, so a real newline
      // inside the quotes would truncate the value at the first line.
      const needsQuotes = /[\s#"=]/.test(v) || v === "";
      const escaped = v
        .replace(/\\/g, "\\\\")
        .replace(/"/g, '\\"')
        .replace(/\n/g, "\\n")
        .replace(/\r/g, "\\r");
      return needsQuotes ? `${r.name}="${escaped}"` : `${r.name}=${v}`;
    })
    .join("\n");
}

// Parse dotenv-ish text. Each non-empty, non-comment line is split on
// the first '=' into key/value. Surrounding double quotes are
// stripped and \" / \\ / \n / \r are unescaped. Anything that doesn't
// match a valid `KEY=value` pattern is silently dropped — the textarea
// is the user's pasteboard, not a strict parser.
export function dotenvToRows(text: string, prevSecrets: Row[]): Row[] {
  const out: Row[] = [];
  const lines = text.split(/\r?\n/);
  for (const raw of lines) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    const name = line.slice(0, eq).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) continue;
    let value = line.slice(eq + 1).trim();
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      // Single pass so an unescaped backslash can't merge with the
      // following char and be re-interpreted (`\\n` must stay `\` + `n`,
      // not become a newline). Unknown escapes pass through untouched.
      const unescape: Record<string, string> = { '"': '"', "\\": "\\", n: "\n", r: "\r" };
      value = value
        .slice(1, -1)
        .replace(/\\(.)/g, (m, c: string) => unescape[c] ?? m);
    }
    out.push({ id: rid(), name, value, fromSecret: false, secretBacked: false, visible: false });
  }
  // Preserve any secret-backed entries — they aren't representable in
  // the bulk textarea, so we re-attach them after parsing so the user
  // doesn't accidentally lose them.
  for (const s of prevSecrets) {
    if (!out.some((r) => r.name === s.name)) out.push(s);
  }
  return out;
}

// rowsShallowEqual is a cheap diff for the conflict detector — same
// length, same name/value/fromSecret tuple per row in order. Catches
// the common cases (added var, edited value, removed var) without
// pulling in lodash.isEqual.
export function rowsShallowEqual(a: Row[], b: Row[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i].name !== b[i].name) return false;
    if (a[i].value !== b[i].value) return false;
    if (a[i].fromSecret !== b[i].fromSecret) return false;
    if (a[i].secretBacked !== b[i].secretBacked) return false;
  }
  return true;
}
