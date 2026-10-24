import { api } from "@/lib/api-client";

// Admin-scope token routes (server-go handlers/tokens_admin.go), gated
// on user:write. Self-service tokens stay in features/profile.

export interface AdminTokenRow {
  id: string;
  name: string;
  createdAt: string;
  expiresAt: string;
  isActive: boolean;
  user: { id: string; username: string; email: string };
}

export interface IssuedToken {
  name: string;
  token: string;
  expiresAt: string;
}

export interface TokenUserOption {
  id: string;
  username: string;
  email: string;
}

export const adminTokensQueryKey = ["tokens", "all"] as const;
export const tokenUserOptionsQueryKey = ["tokens", "user-options"] as const;

export async function listAllTokens(): Promise<AdminTokenRow[]> {
  return (await api<AdminTokenRow[] | null>("/api/tokens")) ?? [];
}

// expiresAt: RFC3339, or "" for the server's 365-day cap.
export async function issueTokenForUser(
  userId: string,
  name: string,
  expiresAt: string
): Promise<IssuedToken> {
  return api<IssuedToken>(`/api/tokens/user/${encodeURIComponent(userId)}`, {
    method: "POST",
    body: { name, expiresAt },
  });
}

export async function revokeAnyToken(id: string): Promise<void> {
  await api<void>(`/api/tokens/${encodeURIComponent(id)}`, { method: "DELETE" });
}

// GET /api/users answers either a bare array or {data: [...]} depending
// on server version; the users page handles both the same way.
export async function listTokenUserOptions(): Promise<TokenUserOption[]> {
  const res = await api<{ data?: TokenUserOption[] } | TokenUserOption[] | null>("/api/users");
  const rows = Array.isArray(res) ? res : (res?.data ?? []);
  return rows.map((u) => ({ id: u.id, username: u.username, email: u.email }));
}
