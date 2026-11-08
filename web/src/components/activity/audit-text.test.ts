import { describe, expect, it } from "vitest";
import { auditActionLabel, humanizeAuditMessage } from "./audit-text";

const names: Record<string, string> = { eee9027aff71396f0c8d1f2f841bd6c1: "dana" };
const nameOf = (id: string) => names[id] ?? null;

describe("auditActionLabel", () => {
  it("maps known ids and words-ifies unknown ones", () => {
    expect(auditActionLabel("service.terminal")).toBe("Opened a terminal");
    expect(auditActionLabel("user.instance-role.update")).toBe("Changed a user's role");
    expect(auditActionLabel("widget.frob_now")).toBe("Widget frob now");
    expect(auditActionLabel("")).toBe("Event");
  });
});

describe("humanizeAuditMessage", () => {
  it("resolves user ids and keeps unknown ones as details", () => {
    const m = humanizeAuditMessage(
      "project.grant.add",
      'granted user id=4e6283a5342079e9ee8cb23bbc67e64d access to project "shop" with role "viewer" (grant id=01936b604e989251)',
      nameOf,
    );
    expect(m.text).toBe('granted user 4e6283a5… access to project "shop" with role "viewer"');
    expect(m.details).toEqual(["grant id=01936b604e989251", "user id=4e6283a5342079e9ee8cb23bbc67e64d"]);
    expect(
      humanizeAuditMessage("x", 'set instance role of user id=eee9027aff71396f0c8d1f2f841bd6c1 to "editor"', nameOf).text,
    ).toBe('set instance role of dana to "editor"');
  });

  it("moves kube internals to details", () => {
    const m = humanizeAuditMessage(
      "build.rollback",
      "rolled production back to build shop-api-c850e9b0ef35-mw883yzn (env CR shop-api-production, force=false)",
      nameOf,
    );
    expect(m.text).toBe("rolled production back to build shop-api-c850e9b0ef35-mw883yzn");
    expect(m.details).toEqual(["env CR shop-api-production, force=false"]);
    expect(
      humanizeAuditMessage("addon.backup_run", "manual backup started for addon demo/demo-db job=demo-db-backup-1", nameOf),
    ).toEqual({ text: "manual backup started for addon demo/demo-db", details: ["job=demo-db-backup-1"] });
    expect(humanizeAuditMessage("addon.sql_query", "[sha256:a9c9] SELECT 1", nameOf)).toEqual({
      text: "SELECT 1",
      details: ["sha256:a9c9"],
    });
  });

  it("tidies secret messages and action echoes", () => {
    expect(humanizeAuditMessage("secret.set", 'set key="STRIPE_SECRET_KEY" env="" (value redacted)', nameOf).text).toBe(
      "set STRIPE_SECRET_KEY (value redacted)",
    );
    expect(humanizeAuditMessage("deployhook.enable", "deployhook.enable for noapp/web-ui", nameOf).text).toBe("noapp/web-ui");
  });
});
