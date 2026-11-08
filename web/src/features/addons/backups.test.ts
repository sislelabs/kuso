import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api-client";
import { backupNowState, backupSummary, backupsListState, describeCron, type BackupsListState } from "./backups";
import type { KusoAddon } from "@/types/projects";

const pg = (spec: Partial<KusoAddon["spec"]> = {}): KusoAddon =>
  ({ metadata: { name: "p-db" }, spec: { project: "p", kind: "postgres", ...spec } }) as KusoAddon;
const ok = (whens: string[]): BackupsListState => ({ kind: "ok", items: whens.map((when, i) => ({ key: `k${i}`, size: 1, when })) });
const now = Date.parse("2026-11-08T12:00:00Z");

describe("backupNowState", () => {
  const scheduled = pg({ backup: { schedule: "0 3 * * *" } });
  it("enables only a scheduled, managed postgres with S3 for editors", () => {
    expect(backupNowState(scheduled, { canWrite: true, list: ok([]) }).enabled).toBe(true);
    expect(backupNowState(scheduled, { canWrite: true, list: { kind: "error", message: "x" } }).enabled).toBe(true);
  });
  it.each([
    ["no schedule", pg(), true, ok([]), /schedule in the Backups tab/],
    ["viewer", scheduled, false, ok([]), /editor access/],
    ["no S3", scheduled, true, { kind: "no-s3" } as const, /Add S3 credentials/],
    ["shared instance", pg({ useInstanceAddon: "pg" }), true, ok([]), /shared instance/],
    ["external", pg({ external: { secretName: "x" } }), true, ok([]), /provider/],
    ["not loaded", undefined, true, ok([]), /Loading/],
    ["list loading", scheduled, true, { kind: "loading" } as const, /Checking/],
  ])("blocks %s and says why", (_n, cr, canWrite, list, reason) => {
    const st = backupNowState(cr, { canWrite, list });
    expect(st.enabled).toBe(false);
    expect(st.reason).toMatch(reason);
  });
});

describe("backupSummary", () => {
  it("reads one sentence per state", () => {
    expect(backupSummary(pg(), ok(["2026-11-04T12:00:00Z"]), now).sentence).toBe("Scheduled backups off · last backup 4d ago");
    expect(backupSummary(pg({ backup: { schedule: "0 3 * * *" } }), ok(["2026-11-08T10:00:00Z", "2026-11-07T10:00:00Z"]), now).sentence).toBe(
      "Scheduled daily at 03:00 · last backup 2h ago",
    );
    expect(backupSummary(pg(), ok([]), now).headline).toBe("No backups yet");
    expect(backupSummary(pg({ useInstanceAddon: "pg" }), ok(["2026-11-08T01:45:00Z"]), now).sentence).toBe(
      "Backed up with pg · last backup 10h ago",
    );
    expect(backupSummary(pg(), { kind: "no-s3" }, now).sentence).toBe("Scheduled backups off · S3 isn't set up");
  });
});

describe("backupsListState", () => {
  it("treats only the not-configured 503 as missing S3", () => {
    const err = (status: number, msg: string) => new ApiError(status, { error: msg }, msg);
    expect(backupsListState({ isPending: false, isError: true, error: err(503, "backup not configured: PUT …") }).kind).toBe("no-s3");
    expect(backupsListState({ isPending: false, isError: true, error: err(503, "s3: dial tcp") }).kind).toBe("error");
  });
});

describe("describeCron", () => {
  it("words common schedules", () => {
    expect(describeCron("30 2 * * 1")).toBe("weekly on Monday at 02:30");
    expect(describeCron("0 */6 * * *")).toBe("every 6h");
    expect(describeCron("0 3 1 * *")).toBe("0 3 1 * *");
  });
});
