import { api } from "@/lib/api-client";

export interface DirectoryUser {
  id: string;
  username: string;
  email?: string;
  firstName?: string;
  lastName?: string;
}

// GET /api/users needs settings:admin or user:write; callers gate on
// that before enabling the query.
export async function listUserDirectory(): Promise<DirectoryUser[]> {
  const res = await api<{ data?: DirectoryUser[] } | DirectoryUser[]>("/api/users");
  return Array.isArray(res) ? res : (res.data ?? []);
}

export type AccessVia = "direct" | "group";

export interface AccessSource {
  via: AccessVia;
  groupId?: string;
  groupName?: string;
}

export interface InstanceRoleSource extends AccessSource {
  role: string;
}

export interface ProjectGrantSource extends AccessSource {
  // What this one grant confers; roleOverride is "" when it inherits the
  // user's instance role.
  role: string;
  roleOverride: string;
}

export interface ProjectAccess {
  project: string;
  role: string;
  sources: ProjectGrantSource[];
}

export interface UserAccessGroup {
  id: string;
  name: string;
  instanceRole: string;
}

// Mirrors GET /api/users/{id}/access. The effective values are computed
// server-side by the same resolver the authorizer uses.
export interface UserAccess {
  userId: string;
  username: string;
  instanceRole: string;
  instanceRoleSources: InstanceRoleSource[];
  groups: UserAccessGroup[];
  allProjects: boolean;
  projects: ProjectAccess[];
  pending: boolean;
}

export async function getUserAccess(userId: string): Promise<UserAccess> {
  return api<UserAccess>(`/api/users/${encodeURIComponent(userId)}/access`);
}
