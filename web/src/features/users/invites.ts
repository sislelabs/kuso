// GET /api/invites returns db.Invite with Go's sql.Null* types serialised
// as-is, hence the {String,Valid} / {Time,Valid} wrappers.
export interface NullString {
  String?: string;
  Valid?: boolean;
}

export interface NullTime {
  Time?: string;
  Valid?: boolean;
}

export interface InviteRow {
  id: string;
  token: string;
  url: string;
  groupId?: NullString | null;
  instanceRole?: NullString | null;
  createdBy: string;
  createdAt: string;
  expiresAt?: NullTime | null;
  maxUses: number;
  usedCount: number;
  revokedAt?: NullTime | null;
  note?: NullString | null;
}

export function nullStr(v: NullString | null | undefined): string {
  if (!v || !v.Valid) return "";
  return v.String ?? "";
}

export function nullTime(v: NullTime | null | undefined): string {
  if (!v || !v.Valid) return "";
  return v.Time ?? "";
}

export type InviteStatus = "active" | "revoked" | "expired" | "used up";

export function inviteStatus(inv: InviteRow, now: number = Date.now()): InviteStatus {
  if (nullTime(inv.revokedAt)) return "revoked";
  const exp = nullTime(inv.expiresAt);
  if (exp && new Date(exp).getTime() <= now) return "expired";
  if (inv.usedCount >= inv.maxUses) return "used up";
  return "active";
}

// Invites that can still be redeemed: not revoked, not expired, seats left.
export function pendingInvites(invites: readonly InviteRow[], now: number = Date.now()): InviteRow[] {
  return invites.filter((inv) => inviteStatus(inv, now) === "active");
}

export function pendingInviteCount(invites: readonly InviteRow[] | undefined, now: number = Date.now()): number {
  return invites ? pendingInvites(invites, now).length : 0;
}
