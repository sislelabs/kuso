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
