import { describe, expect, it } from "vitest";
import type { Row } from "@/components/service/envVarTransforms";
import { isSensitiveKey, isSensitiveVar, valueLooksSecret } from "./sensitivity";
import { buildEnvDiff, parseRawEnv, reconcileRaw, rowsToRaw, HIDDEN_MARKER, READONLY_MARKER } from "./rawEnv";
import { applyRefPick, findRefTrigger, rankMatches } from "./refAutocomplete";
import { restartImpactFromEnvs, restartNote } from "./restartImpact";

const row = (name: string, value: string, extra: Partial<Row> = {}): Row => ({
  id: name,
  name,
  value,
  fromSecret: false,
  secretBacked: false,
  visible: false,
  origName: name,
  ...extra,
});

describe("isSensitiveKey", () => {
  it.each([
    "API_KEY",
    "OPENAI_API_KEY",
    "GITHUB_SECRET",
    "NEXTAUTH_SECRET",
    "DB_PASSWORD",
    "SMTP_PASS",
    "MYSQL_PWD",
    "SENTRY_DSN",
    "GOOGLE_CREDENTIALS",
    "JWT_PRIVATE_KEY",
    "BASIC_AUTH",
    "SESSION_COOKIE",
    "HASH_SALT",
    "STRIPE_TOKEN",
    "SLACK_WEBHOOK_URL",
    "api_key",
  ])("masks %s", (k) => expect(isSensitiveKey(k)).toBe(true));

  it.each([
    "KEYCLOAK_URL",
    "PASSPORT_MODE",
    "NODE_ENV",
    "GITHUB_ID",
    "INGEST_URL",
    "NEXTAUTH_URL",
    "AUTH_URL",
    "COOKIE_DOMAIN",
    "TOKEN_TTL",
    "NEXT_PUBLIC_MAPBOX_TOKEN",
    "VITE_API_KEY",
    "MONKEY_COUNT",
    "",
  ])("shows %s", (k) => expect(isSensitiveKey(k)).toBe(false));
});

describe("valueLooksSecret", () => {
  it("flags URLs carrying a password", () => {
    expect(valueLooksSecret("postgres://app:hunter2@db:5432/app")).toBe(true);
    expect(valueLooksSecret("redis://:pw@cache:6379")).toBe(true);
  });
  it("leaves plain URLs and user-only URLs alone", () => {
    expect(valueLooksSecret("https://api.example.com/v1")).toBe(false);
    expect(valueLooksSecret("ssh://git@github.com/x")).toBe(false);
    expect(valueLooksSecret("https://x.dev/#/a@b")).toBe(false);
  });
  it("flags PEM private keys", () => {
    expect(valueLooksSecret("-----BEGIN RSA PRIVATE KEY-----\nabc")).toBe(true);
  });
  it("combines key and value", () => {
    expect(isSensitiveVar("DATABASE_URL", "postgres://u:p@h/db")).toBe(true);
    expect(isSensitiveVar("DATABASE_URL", "postgres://h/db")).toBe(false);
  });
});

describe("parseRawEnv", () => {
  it("handles export, comments, blanks, quotes and inline comments", () => {
    const { entries, errors } = parseRawEnv(
      [
        "# header",
        "",
        "export A=1",
        'B="two words"',
        "C='lit\\n'",
        "D=http://x/#frag # trailing",
        'E="esc\\n\\"q\\""',
        "F=",
        "G= spaced ",
      ].join("\n"),
    );
    expect(errors).toEqual([]);
    expect(Object.fromEntries(entries.map((e) => [e.name, e.value]))).toEqual({
      A: "1",
      B: "two words",
      C: "lit\\n",
      D: "http://x/#frag",
      E: 'esc\n"q"',
      F: "",
      G: "spaced",
    });
  });

  it("reads multi-line quoted values and keeps line numbers right", () => {
    const { entries, errors } = parseRawEnv('A=1\nKEY="line1  \nline2"\nB=2');
    expect(errors).toEqual([]);
    expect(entries).toEqual([
      { name: "A", value: "1", line: 1 },
      { name: "KEY", value: "line1  \nline2", line: 2 },
      { name: "B", value: "2", line: 4 },
    ]);
  });

  it("reports malformed lines with line numbers instead of dropping them", () => {
    const { errors } = parseRawEnv("A=1\nNOEQUALS\n1BAD=x\n=x\nFOO BAR=1\nA=2\nX=\"open");
    expect(errors.map((e) => e.line)).toEqual([2, 3, 4, 5, 6, 7]);
    expect(errors[4].message).toMatch(/already set on line 1/);
    expect(errors[5].message).toMatch(/unterminated/);
  });
});

describe("rowsToRaw / reconcileRaw", () => {
  const opaque = row("MOUNTED", "", { fromSecret: true, secretBacked: true, origValueFrom: { secretKeyRef: { name: "x", key: "y" } } });
  const addon = row("POSTGRES_HOST", "db", { secretBacked: true, addon: "db" });
  const managed = row("API_TOKEN", "s3cret", { secretBacked: true, managed: true });
  const ref = row("DATABASE_URL", "${{ db.DATABASE_URL }}", { secretBacked: true });
  const plain = row("NODE_ENV", "production");
  const rows = [plain, opaque, addon, managed, ref];

  it("round-trips without changes and keeps read-only rows as comments", () => {
    const text = rowsToRaw(rows);
    expect(text).toContain("DATABASE_URL=${{ db.DATABASE_URL }}");
    expect(text).toContain(`# MOUNTED=${READONLY_MARKER}`);
    expect(text).toContain(`# POSTGRES_HOST=${READONLY_MARKER}`);
    expect(text).toContain("API_TOKEN=s3cret");
    const res = reconcileRaw(text, rows);
    expect(res.errors).toEqual([]);
    expect(new Set(res.rows)).toEqual(new Set(rows));
  });

  it("keeps read-only rows when their comment is deleted", () => {
    const res = reconcileRaw("NODE_ENV=production\nAPI_TOKEN=s3cret\nDATABASE_URL=${{ db.DATABASE_URL }}", rows);
    expect(res.rows.map((r) => r.name).sort()).toEqual(
      ["API_TOKEN", "DATABASE_URL", "MOUNTED", "NODE_ENV", "POSTGRES_HOST"],
    );
  });

  it("adds, changes and removes editable rows, preserving ids and flags", () => {
    const res = reconcileRaw("NODE_ENV=staging\nNEW=1\nDATABASE_URL=${{ db.DATABASE_URL }}", rows);
    const by = Object.fromEntries(res.rows.map((r) => [r.name, r]));
    expect(by.NODE_ENV.id).toBe("NODE_ENV");
    expect(by.NODE_ENV.value).toBe("staging");
    expect(by.NEW.value).toBe("1");
    expect(by.API_TOKEN).toBeUndefined();
    expect(by.DATABASE_URL).toBe(ref);
  });

  it("rejects an uncommented read-only marker", () => {
    const res = reconcileRaw(`MOUNTED=${READONLY_MARKER}`, rows);
    expect(res.errors[0]).toMatchObject({ line: 1 });
    expect(res.rows).toBe(rows);
  });

  it("hides secret values behind a placeholder that keeps them on save", () => {
    const text = rowsToRaw(rows, { hideSecrets: true });
    expect(text).toContain(`API_TOKEN=${HIDDEN_MARKER}`);
    expect(text).not.toContain("s3cret");
    expect(text).toContain("NODE_ENV=production");
    expect(text).toContain("DATABASE_URL=${{ db.DATABASE_URL }}");
    const kept = reconcileRaw(text, rows);
    expect(kept.errors).toEqual([]);
    expect(kept.rows.find((r) => r.name === "API_TOKEN")).toBe(managed);
    const changed = reconcileRaw(text.replace(`API_TOKEN=${HIDDEN_MARKER}`, "API_TOKEN=rotated"), rows);
    expect(changed.rows.find((r) => r.name === "API_TOKEN")?.value).toBe("rotated");
  });

  it("rejects the placeholder on a new name and never writes it over a read-only row", () => {
    expect(reconcileRaw(`BRAND_NEW=${HIDDEN_MARKER}`, rows).errors[0]).toMatchObject({ line: 1 });
    const res = reconcileRaw(`POSTGRES_HOST=${HIDDEN_MARKER}`, rows);
    expect(res.rows.find((r) => r.name === "POSTGRES_HOST")).toBe(addon);
  });

  it("serialises values that need quoting so they parse back identically", () => {
    const tricky = [row("A", "multi\nline"), row("B", " padded "), row("C", "x #y"), row("D", '"q')];
    const res = reconcileRaw(rowsToRaw(tricky), tricky);
    expect(res.errors).toEqual([]);
    expect(res.rows).toEqual(tricky);
  });
});

describe("buildEnvDiff", () => {
  it("splits the plan into added/changed/removed and masks secrets", () => {
    const baseline = [row("NODE_ENV", "production"), row("API_TOKEN", "old"), row("GONE", "x")];
    const rows = [row("NODE_ENV", "staging"), row("API_TOKEN", "new"), row("NEW_URL", "https://x")];
    const d = buildEnvDiff(
      {
        valueWrites: [
          { name: "NODE_ENV", value: "staging" },
          { name: "API_TOKEN", value: "new" },
          { name: "NEW_URL", value: "https://x" },
        ],
        deletes: ["GONE"],
      },
      baseline,
      rows,
    );
    expect(d.added).toEqual([{ name: "NEW_URL", after: "https://x", masked: false }]);
    expect(d.changed[0]).toMatchObject({ name: "API_TOKEN", masked: true });
    expect(d.changed[0].before).not.toContain("old");
    expect(d.changed[0].after).not.toContain("new");
    expect(d.changed[0].before).not.toEqual(d.changed[0].after);
    expect(d.changed[1]).toEqual({ name: "NODE_ENV", before: "production", after: "staging", masked: false });
    expect(d.removed).toEqual([{ name: "GONE", before: "x", masked: false }]);
  });
});

describe("ref autocomplete", () => {
  it("finds the open ref under the caret", () => {
    expect(findRefTrigger("${{", 3)).toEqual({ start: 0, query: "", target: "", key: null });
    expect(findRefTrigger("x ${{ d", 7)).toEqual({ start: 2, query: "d", target: "d", key: null });
    expect(findRefTrigger("${{ db.DA", 9)).toEqual({ start: 0, query: "db.DA", target: "db", key: "DA" });
    expect(findRefTrigger("${{ db.X }}", 11)).toBeNull();
    expect(findRefTrigger("plain", 5)).toBeNull();
  });

  it("inserts a target then a key, swallowing a typed closer", () => {
    const t1 = findRefTrigger("${{ d", 5)!;
    const s1 = applyRefPick("${{ d", 5, t1, { target: "db" });
    expect(s1).toEqual({ value: "${{ db.", caret: 7 });
    const v = "pre ${{ db.DA }} post";
    const t2 = findRefTrigger(v, 13)!;
    expect(applyRefPick(v, 13, t2, { target: "db", key: "DATABASE_URL" }).value).toBe(
      "pre ${{ db.DATABASE_URL }} post",
    );
  });

  it("ranks prefix matches first", () => {
    expect(rankMatches(["POOLER_URL", "DATABASE_URL", "URL"], "url", (s) => s)).toEqual([
      "URL",
      "POOLER_URL",
      "DATABASE_URL",
    ]);
  });
});

describe("restart impact", () => {
  const env = (service: string, name: string, ready?: number) => ({
    name,
    spec: { service },
    status: ready === undefined ? undefined : { replicas: { ready } },
  });
  it("sums ready pods across this service's envs only", () => {
    const envs = [env("p-web", "production", 2), env("web", "preview-pr-3", 1), env("p-api", "production", 5), env("p-web", "staging", 0), env("p-web", "x")];
    const impact = restartImpactFromEnvs(envs, "p", "web", (e) => e.name);
    expect(impact).toEqual({ pods: 3, envs: ["preview-pr-3", "production"] });
    expect(restartNote(impact)).toBe(
      "Deploying restarts 3 running pods (preview-pr-3, production) once. Variables can't be applied without a restart.",
    );
  });
  it("says so when nothing runs", () => {
    expect(restartNote({ pods: 0, envs: [] })).toMatch(/No pods are running/);
  });
});
