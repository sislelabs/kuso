import { api } from "@/lib/api-client";
import type { UserProfile } from "@/types/api";

export async function getMyProfile(): Promise<UserProfile> {
  return api("/api/users/profile");
}

export interface UpdateProfileBody {
  firstName?: string;
  lastName?: string;
  email?: string;
}

export interface ChangePasswordBody {
  currentPassword: string;
  newPassword: string;
}

export async function updateProfile(body: UpdateProfileBody): Promise<UserProfile> {
  return api("/api/users/profile", { method: "PUT", body });
}

export async function changePassword(body: ChangePasswordBody): Promise<void> {
  return api("/api/users/profile/password", { method: "PUT", body });
}

// Admin reset of another user's password: no current-password check.
export async function resetUserPassword(userId: string, password: string): Promise<void> {
  return api(`/api/users/id/${encodeURIComponent(userId)}/password`, {
    method: "PUT",
    body: { password },
  });
}

export interface TokenSummary {
  id: string;
  name: string;
  createdAt: string;
  expiresAt: string;
  isActive: boolean;
}

export interface IssueTokenResponse {
  name: string;
  token: string;
  expiresAt: string;
}

export async function listMyTokens(): Promise<TokenSummary[]> {
  return api("/api/tokens/my");
}

// expiresAt = "" or "never" mints a non-expiring token. Server omits
// the JWT exp claim and stores a 100y sentinel in the DB row.
export async function issueMyToken(
  name: string,
  expiresAt: string
): Promise<IssueTokenResponse> {
  return api("/api/tokens/my", { method: "POST", body: { name, expiresAt } });
}

export async function revokeMyToken(id: string): Promise<void> {
  return api(`/api/tokens/my/${encodeURIComponent(id)}`, { method: "DELETE" });
}

// POST /api/users/profile/avatar reads at most 1 MiB of the file and the
// router caps the whole multipart body at 1 MiB, so leave headroom for
// the form envelope. The server sniffs the bytes and accepts only these.
export const AVATAR_MAX_BYTES = 1000 * 1024;
export const AVATAR_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"] as const;

export function validateAvatarFile(file: { size: number; type: string }): string | null {
  if (!(AVATAR_TYPES as readonly string[]).includes(file.type)) {
    return "Avatar must be PNG, JPEG, GIF or WEBP.";
  }
  if (file.size === 0) return "File is empty.";
  if (file.size > AVATAR_MAX_BYTES) {
    return `Avatar is ${Math.ceil(file.size / 1024)} KiB; the limit is ${AVATAR_MAX_BYTES / 1024} KiB.`;
  }
  return null;
}

export async function uploadAvatar(file: Blob): Promise<void> {
  const form = new FormData();
  form.append("avatar", file);
  await api<{ ok: boolean }>("/api/users/profile/avatar", { method: "POST", body: form });
}
