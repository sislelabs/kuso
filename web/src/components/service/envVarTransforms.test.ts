import { describe, expect, it } from "vitest";
import type { KusoEnvVar } from "@/types/projects";
import {
  addonRefFromValueFrom,
  addonShortByConnSecret,
  dotenvToRows,
  literalToRef,
  reservedEnvWarning,
  rowDiffLabel,
  rowsShallowEqual,
  rowsToDotenv,
  shortServiceFromLabel,
  stripProjectPrefix,
  toRow,
  type Row,
} from "./envVarTransforms";

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const PROJECT = "proj";
const SCOPES = ["production", "staging", "preview-pr-7"];

// One addon with a populated connectionSecret status, one fresh addon
// (no status yet — falls back to canonical "<fqn>-conn" naming).
const addons = [
  {
    metadata: { name: "proj-postgres" },
    status: { connectionSecret: "proj-postgres-conn" },
  },
  { metadata: { name: "proj-redis" } },
];
const addonByConn = addonShortByConnSecret(addons, PROJECT);

function displayRow(v: KusoEnvVar): Row {
  return toRow(v, PROJECT, addonByConn, SCOPES);
}

// saveShape mirrors what the editor's save path hands back to the
// server for a row (see EnvVarsEditor.cleanRows/applyPending):
//   - fromSecret rows are untouched → their original valueFrom is what
//     stays on the server (re-emitted verbatim via origValueFrom);
//   - everything else is a per-key {value} write of the DISPLAY string
//     (the server re-expands ${{ }} refs into secretKeyRefs / DNS).
function saveShape(r: Row): KusoEnvVar {
  if (r.fromSecret) return { name: r.name, valueFrom: r.origValueFrom };
  return { name: r.name, value: r.value };
}

// ---------------------------------------------------------------------------
// Round-trip: server shape → display form → server shape
// ---------------------------------------------------------------------------

describe("round-trip losslessness", () => {
  it("plain literals pass through unchanged", () => {
    const literals = [
      "hello",
      "0",
      "false",
      "postgres://u:p@host:5432/db?sslmode=disable",
      "  leading and trailing  ",
      "with\ttabs",
      "üñí©ödé 🚀 值",
      "line1\nline2\nline3", // multiline survives the ROWS path
      "contains ${{ not a real ref }}",
      "${{", // bare opener
      'quotes " and \' and \\ backslash',
      "https://external.example.com:8443/path", // https never reverses
      "svc.cluster.local", // DNS-ish but not the resolved shape
    ];
    for (const value of literals) {
      const row = displayRow({ name: "V", value });
      expect(row.fromSecret).toBe(false);
      expect(row.secretBacked).toBe(false);
      expect(saveShape(row)).toEqual({ name: "V", value });
    }
  });

  it("empty-string literal stays an empty editable row", () => {
    const row = displayRow({ name: "EMPTY", value: "" });
    expect(row.value).toBe("");
    expect(row.fromSecret).toBe(false);
    expect(row.secretBacked).toBe(false);
  });

  it("addon secretKeyRef renders as ${{ addon.KEY }} (status-backed secret name)", () => {
    const row = displayRow({
      name: "DATABASE_URL",
      valueFrom: {
        secretKeyRef: { name: "proj-postgres-conn", key: "DATABASE_URL" },
      },
    });
    expect(row.value).toBe("${{ postgres.DATABASE_URL }}");
    expect(row.fromSecret).toBe(false); // editable, not opaque
    expect(row.secretBacked).toBe(true); // eye/reveal affordance
    // The display string is exactly what the user typed originally, so
    // the server's re-expansion on save reproduces the same secretKeyRef.
    expect(saveShape(row)).toEqual({
      name: "DATABASE_URL",
      value: "${{ postgres.DATABASE_URL }}",
    });
  });

  it("addon secretKeyRef resolves via canonical <fqn>-conn fallback before status lands", () => {
    const row = displayRow({
      name: "REDIS_URL",
      valueFrom: { secretKeyRef: { name: "proj-redis-conn", key: "REDIS_URL" } },
    });
    expect(row.value).toBe("${{ redis.REDIS_URL }}");
  });

  it("unknown secretKeyRef (not an addon conn secret) passes through unmangled", () => {
    const vf = { secretKeyRef: { name: "my-manual-secret", key: "TOKEN" } };
    const row = displayRow({ name: "TOKEN", valueFrom: vf });
    expect(row.fromSecret).toBe(true);
    expect(row.secretBacked).toBe(true);
    // The exact valueFrom blob is stashed verbatim so save re-emits it.
    expect(row.origValueFrom).toBe(vf);
    expect(saveShape(row)).toEqual({ name: "TOKEN", valueFrom: vf });
  });

  it("non-secretKeyRef valueFrom (fieldRef etc.) passes through unmangled", () => {
    const vf = { fieldRef: { fieldPath: "status.podIP" } };
    const row = displayRow({ name: "POD_IP", valueFrom: vf });
    expect(row.fromSecret).toBe(true);
    expect(row.origValueFrom).toBe(vf);
    expect(saveShape(row)).toEqual({ name: "POD_IP", valueFrom: vf });
  });

  it("revealed opaque secret keeps its valueFrom, not the plaintext", () => {
    // A ?reveal=true read populates `value` alongside valueFrom for an
    // opaque ref; save must still re-emit the ref, never the plaintext.
    const vf = { secretKeyRef: { name: "my-manual-secret", key: "TOKEN" } };
    const row = displayRow({ name: "TOKEN", value: "s3cret", valueFrom: vf });
    expect(row.fromSecret).toBe(true);
    expect(row.value).toBe("s3cret"); // shown to the eye…
    expect(saveShape(row)).toEqual({ name: "TOKEN", valueFrom: vf }); // …not saved
  });

  it("managed-secret rows are editable secret-backed values", () => {
    const row = displayRow({ name: "API_KEY", source: "managed-secret" } as KusoEnvVar);
    expect(row.managed).toBe(true);
    expect(row.secretBacked).toBe(true);
    expect(row.fromSecret).toBe(false);
    expect(row.value).toBe("");
  });

  it("resolved service URL literal reverses to ${{ svc.URL }}", () => {
    const row = displayRow({
      name: "API_URL",
      value: "http://proj-api-production.kuso.svc.cluster.local:3000",
    });
    expect(row.value).toBe("${{ api.URL }}");
    expect(saveShape(row)).toEqual({ name: "API_URL", value: "${{ api.URL }}" });
  });

  it("resolved service HOST literal reverses to ${{ svc.HOST }}", () => {
    const row = displayRow({
      name: "API_HOST",
      value: "proj-api-production.kuso.svc.cluster.local",
    });
    expect(row.value).toBe("${{ api.HOST }}");
  });

  it("resolved service PORT literal is indistinguishable from a number and passes through", () => {
    // ${{ svc.PORT }} resolves server-side to a bare port number; a bare
    // "3000" cannot be safely reversed, so it must stay a literal (the
    // server keeps treating it as a literal → still lossless).
    const row = displayRow({ name: "API_PORT", value: "3000" });
    expect(row.value).toBe("3000");
  });

  it("mixed list round-trips every form at once", () => {
    const server: KusoEnvVar[] = [
      { name: "PLAIN", value: "v1" },
      {
        name: "DB",
        valueFrom: { secretKeyRef: { name: "proj-postgres-conn", key: "URL" } },
      },
      { name: "API", value: "http://proj-api-production.kuso.svc.cluster.local:3000" },
      { name: "OPAQUE", valueFrom: { secretKeyRef: { name: "vendor-secret", key: "K" } } },
      { name: "EMPTY", value: "" },
    ];
    const rows = server.map(displayRow);
    expect(rows.map((r) => r.value)).toEqual([
      "v1",
      "${{ postgres.URL }}",
      "${{ api.URL }}",
      "",
      "",
    ]);
    expect(rows.map(saveShape)).toEqual([
      { name: "PLAIN", value: "v1" },
      { name: "DB", value: "${{ postgres.URL }}" },
      { name: "API", value: "${{ api.URL }}" },
      { name: "OPAQUE", valueFrom: server[3].valueFrom },
      { name: "EMPTY", value: "" },
    ]);
  });
});

// ---------------------------------------------------------------------------
// literalToRef / scope stripping edge cases
// ---------------------------------------------------------------------------

describe("literalToRef", () => {
  it("strips a known non-production scope", () => {
    expect(
      literalToRef("http://proj-api-staging.kuso.svc.cluster.local:8080", PROJECT, SCOPES)
    ).toBe("${{ api.URL }}");
  });

  it("strips multi-segment preview scopes", () => {
    expect(
      literalToRef("proj-api-preview-pr-7.kuso.svc.cluster.local", PROJECT, SCOPES)
    ).toBe("${{ api.HOST }}");
  });

  it("defaults to stripping -production when no scope list is available", () => {
    expect(
      literalToRef("http://proj-api-production.kuso.svc.cluster.local:3000", PROJECT)
    ).toBe("${{ api.URL }}");
  });

  it("keeps a dash-bearing service name intact", () => {
    expect(
      literalToRef("proj-my-worker-production.kuso.svc.cluster.local", PROJECT, SCOPES)
    ).toBe("${{ my-worker.HOST }}");
  });

  it("does not empty out a service literally named after a scope", () => {
    // Service short name "production" → label "proj-production-production"
    // strips ONE scope segment and keeps the service name.
    expect(shortServiceFromLabel("proj-production-production", PROJECT, SCOPES)).toBe(
      "production"
    );
    // And a label that IS just "<project>-<scope>" keeps the scope as name.
    expect(shortServiceFromLabel("proj-production", PROJECT, SCOPES)).toBe("production");
  });

  it("ignores https, external hosts, and embedded matches", () => {
    for (const v of [
      "https://proj-api-production.kuso.svc.cluster.local:3000", // https not cluster-resolved
      "http://example.com",
      "prefix http://proj-api-production.kuso.svc.cluster.local:3000", // not anchored
      "proj-api-production.kuso.svc.cluster.local/path", // trailing path
    ]) {
      expect(literalToRef(v, PROJECT, SCOPES)).toBe(v);
    }
  });

  it("leaves the empty string alone", () => {
    expect(literalToRef("", PROJECT, SCOPES)).toBe("");
  });
});

describe("stripProjectPrefix", () => {
  it("strips only its own project prefix", () => {
    expect(stripProjectPrefix("proj-api", "proj")).toBe("api");
    expect(stripProjectPrefix("other-api", "proj")).toBe("other-api");
    expect(stripProjectPrefix("proj", "proj")).toBe("proj");
  });
});

// ---------------------------------------------------------------------------
// addon conn-secret indexing
// ---------------------------------------------------------------------------

describe("addonShortByConnSecret", () => {
  it("maps status secret and canonical fallback to the short name", () => {
    expect(addonByConn.get("proj-postgres-conn")).toBe("postgres");
    expect(addonByConn.get("proj-redis-conn")).toBe("redis");
  });

  it("keeps a foreign (unprefixed) addon name as-is", () => {
    const m = addonShortByConnSecret(
      [{ metadata: { name: "standalone" }, status: { connectionSecret: "standalone-conn" } }],
      PROJECT
    );
    expect(m.get("standalone-conn")).toBe("standalone");
  });
});

describe("addonRefFromValueFrom", () => {
  it("returns empty for missing/partial/unknown refs", () => {
    expect(addonRefFromValueFrom(undefined, addonByConn)).toBe("");
    expect(addonRefFromValueFrom({}, addonByConn)).toBe("");
    expect(addonRefFromValueFrom({ secretKeyRef: { name: "proj-postgres-conn" } }, addonByConn)).toBe("");
    expect(addonRefFromValueFrom({ secretKeyRef: { key: "URL" } }, addonByConn)).toBe("");
    expect(
      addonRefFromValueFrom({ secretKeyRef: { name: "not-an-addon", key: "URL" } }, addonByConn)
    ).toBe("");
  });
});

// ---------------------------------------------------------------------------
// dotenv bulk-mode round trip
// ---------------------------------------------------------------------------

function literalRow(name: string, value: string): Row {
  return { id: name, name, value, fromSecret: false, secretBacked: false, visible: false };
}

describe("dotenv round trip (bulk mode)", () => {
  it("single-line values round-trip through serialize → parse", () => {
    const rows = [
      literalRow("SIMPLE", "abc"),
      literalRow("WITH_SPACES", "a b c"),
      literalRow("WITH_EQ", "a=b=c"),
      literalRow("WITH_HASH", "not # a comment"),
      literalRow("WITH_QUOTES", 'say "hi"'),
      literalRow("WITH_BACKSLASH", "C:\\path\\to"),
      literalRow("UNICODE", "üñí©ödé🚀"),
      literalRow("EMPTY", ""),
      literalRow("REF", "${{ postgres.DATABASE_URL }}"),
    ];
    const parsed = dotenvToRows(rowsToDotenv(rows), []);
    expect(parsed.map((r) => [r.name, r.value])).toEqual(rows.map((r) => [r.name, r.value]));
  });

  // Multiline values must survive the bulk round trip: the emitter
  // writes them as \n escapes inside the quotes (the parser is
  // line-based, so a real newline inside a quoted value would truncate
  // it at the first line).
  it("multiline values survive the bulk round trip", () => {
    const rows = [literalRow("MULTI", "line1\nline2")];
    const parsed = dotenvToRows(rowsToDotenv(rows), []);
    expect(parsed).toHaveLength(1);
    expect(parsed[0].value).toBe("line1\nline2");
  });

  it("literal backslash-n text is not confused with a newline escape", () => {
    const rows = [literalRow("LITERAL", "not\\na newline"), literalRow("CRLF", "a\r\nb")];
    const parsed = dotenvToRows(rowsToDotenv(rows), []);
    expect(parsed.map((r) => r.value)).toEqual(["not\\na newline", "a\r\nb"]);
  });

  it("secret-backed rows are commented out and re-attached, never rewritten", () => {
    const secret: Row = {
      id: "s",
      name: "TOKEN",
      value: "",
      fromSecret: true,
      secretBacked: true,
      visible: false,
      origValueFrom: { secretKeyRef: { name: "vendor", key: "TOKEN" } },
    };
    const rows = [literalRow("PLAIN", "v"), secret];
    const text = rowsToDotenv(rows);
    expect(text).toContain("# TOKEN=<from secret>");
    const parsed = dotenvToRows(text, [secret]);
    // PLAIN parsed back as a literal; TOKEN re-attached as the SAME row.
    expect(parsed.find((r) => r.name === "PLAIN")?.value).toBe("v");
    expect(parsed.find((r) => r.name === "TOKEN")).toBe(secret);
  });

  it("drops junk lines instead of inventing rows", () => {
    const parsed = dotenvToRows(
      ["# comment", "", "NOVALUE", "=nokey", "9BAD=x", "OK=fine"].join("\n"),
      []
    );
    expect(parsed.map((r) => [r.name, r.value])).toEqual([["OK", "fine"]]);
  });
});

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

describe("rowDiffLabel", () => {
  it("never leaks secret plaintext", () => {
    expect(
      rowDiffLabel({ ...literalRow("A", "irrelevant"), fromSecret: true })
    ).toBe("<secret>");
    const label = rowDiffLabel({
      ...literalRow("B", "retyped-plaintext"),
      secretBacked: true,
    });
    expect(label).not.toContain("retyped-plaintext");
    expect(label.startsWith("•••••")).toBe(true);
    // An untouched (blank) secret-backed row stays the bare placeholder —
    // that's what marks it "not being rewritten" in the diff.
    expect(
      rowDiffLabel({ ...literalRow("B", ""), secretBacked: true })
    ).toBe("•••••");
  });

  // Regression: rowDiffLabel used to return a CONSTANT "•••••" for every
  // secret-backed row, so rotating a key (pasting a new secret over an old
  // one) diffed as identical — the confirm dialog reported "No effective
  // changes detected" and silently dropped the save.
  it("distinguishes two different secret values", () => {
    const a = rowDiffLabel({ ...literalRow("K", "sk-old-key-value"), secretBacked: true });
    const b = rowDiffLabel({ ...literalRow("K", "sk-new-key-value"), secretBacked: true });
    expect(a).not.toBe(b);
    // Same length, different content — the exact rotation case a
    // length-based label would have missed.
    expect(a).not.toContain("sk-old");
    expect(b).not.toContain("sk-new");
  });

  it("shows refs verbatim and clips long values", () => {
    expect(rowDiffLabel(literalRow("C", "${{ postgres.URL }}"))).toBe("${{ postgres.URL }}");
    const long = "x".repeat(80);
    expect(rowDiffLabel(literalRow("D", long))).toBe("x".repeat(57) + "…");
    expect(rowDiffLabel(literalRow("E", "short"))).toBe("short");
  });
});

describe("reservedEnvWarning", () => {
  it("flags reserved names and passes normal ones", () => {
    expect(reservedEnvWarning("PORT")).toMatch(/PORT/);
    expect(reservedEnvWarning("HOSTNAME")).toMatch(/kubelet/);
    expect(reservedEnvWarning("KUBERNETES_SERVICE_HOST")).toMatch(/KUBERNETES_/);
    expect(reservedEnvWarning("DATABASE_URL")).toBe("");
    expect(reservedEnvWarning("")).toBe("");
  });
});

describe("rowsShallowEqual", () => {
  it("detects add/edit/remove and ignores UI-only fields", () => {
    const a = [literalRow("A", "1"), literalRow("B", "2")];
    expect(rowsShallowEqual(a, [literalRow("A", "1"), literalRow("B", "2")])).toBe(true);
    expect(rowsShallowEqual(a, [literalRow("A", "1")])).toBe(false);
    expect(rowsShallowEqual(a, [literalRow("A", "1"), literalRow("B", "changed")])).toBe(false);
    // visible/id differences don't count
    const b = a.map((r) => ({ ...r, id: r.id + "x", visible: true }));
    expect(rowsShallowEqual(a, b)).toBe(true);
  });
});
