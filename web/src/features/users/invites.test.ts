import { describe, expect, it } from "vitest";
import { inviteStatus, pendingInviteCount, pendingInvites, type InviteRow } from "./invites";
import { hasNeverSignedIn, lastSeenLabel } from "./presence";

const NOW = Date.parse("2026-11-08T12:00:00Z");
const iso = (offsetSec: number) => new Date(NOW + offsetSec * 1000).toISOString();

function invite(over: Partial<InviteRow> = {}): InviteRow {
  return {
    id: "i",
    token: "t",
    url: "https://kuso.example/invite/t",
    createdBy: "u",
    createdAt: iso(-3600),
    maxUses: 1,
    usedCount: 0,
    expiresAt: { Time: iso(86400), Valid: true },
    revokedAt: { Valid: false },
    ...over,
  };
}

describe("pendingInvites", () => {
  it("counts only unexpired, unused, unrevoked invites", () => {
    const list = [
      invite({ id: "live" }),
      invite({ id: "no-expiry", expiresAt: null }),
      invite({ id: "multi-partly-used", maxUses: 5, usedCount: 2 }),
      invite({ id: "expired", expiresAt: { Time: iso(-1), Valid: true } }),
      invite({ id: "redeemed", usedCount: 1 }),
      invite({ id: "revoked", revokedAt: { Time: iso(-60), Valid: true } }),
    ];
    expect(pendingInvites(list, NOW).map((i) => i.id)).toEqual(["live", "no-expiry", "multi-partly-used"]);
    expect(pendingInviteCount(list, NOW)).toBe(3);
  });

  it("treats a missing list as zero", () => {
    expect(pendingInviteCount(undefined, NOW)).toBe(0);
  });

  it("reports why an invite is dead, revoked first", () => {
    expect(inviteStatus(invite({ revokedAt: { Time: iso(-1), Valid: true }, usedCount: 1 }), NOW)).toBe("revoked");
    expect(inviteStatus(invite({ expiresAt: { Time: iso(-1), Valid: true } }), NOW)).toBe("expired");
    expect(inviteStatus(invite({ usedCount: 1 }), NOW)).toBe("used up");
  });
});

describe("lastSeenLabel", () => {
  it("reads active, last seen, or never", () => {
    expect(lastSeenLabel(iso(-60), NOW)).toBe("Active now");
    expect(lastSeenLabel(iso(5), NOW)).toBe("Active now");
    expect(lastSeenLabel(iso(-5 * 60), NOW)).toBe("Last seen 5m ago");
    expect(lastSeenLabel(iso(-2 * 86400), NOW)).toBe("Last seen 2d ago");
    expect(lastSeenLabel(undefined, NOW)).toBe("Never signed in");
    expect(lastSeenLabel(null, NOW)).toBe("Never signed in");
    expect(lastSeenLabel(null, NOW, "github")).toBe("Last seen unknown");
  });
});

describe("hasNeverSignedIn", () => {
  it("only flags local accounts: an OAuth account exists because it signed in", () => {
    expect(hasNeverSignedIn({ lastLogin: null, provider: "local" })).toBe(true);
    expect(hasNeverSignedIn({ lastLogin: null })).toBe(true);
    expect(hasNeverSignedIn({ lastLogin: null, provider: "github" })).toBe(false);
    expect(hasNeverSignedIn({ lastLogin: "2026-11-08T00:00:00Z", provider: "local" })).toBe(false);
  });
});
