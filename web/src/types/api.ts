// API response shapes used in Phase A. Mirrors the Go server's JSON.

export interface AuthMethods {
  local: boolean;
  github: boolean;
  oauth2: boolean;
}

export interface AuthSession {
  isAuthenticated: boolean;
  userId: string;
  username: string;
  role: string;
  userGroups: string[];
  permissions: string[];
  adminDisabled: boolean;
  templatesEnabled: boolean;
  consoleEnabled: boolean;
  metricsEnabled: boolean;
  sleepEnabled: boolean;
  auditEnabled: boolean;
  buildPipeline: boolean;
}

export interface LoginResponse {
  access_token: string;
}

export interface UserProfile {
  id: string;
  username: string;
  email: string;
  firstName: string | null;
  lastName: string | null;
  role: string;
  userGroups: string[];
  permissions: string[];
  image?: string | null;
  // Role-system v2: instance perms live in `permissions` (admin-only);
  // project access is resolved server-side and surfaced here so the
  // client can gate per-project affordances without project perms in
  // the JWT.
  instanceRole?: "admin" | "editor" | "viewer" | "";
  // adminAll = instance admin: sees/acts on every project as admin.
  adminAll?: boolean;
  // projectRoles maps a visible project name → the user's effective
  // role there. Empty/absent for ungranted projects. Irrelevant when
  // adminAll is true.
  projectRoles?: Record<string, "admin" | "editor" | "viewer">;
}
