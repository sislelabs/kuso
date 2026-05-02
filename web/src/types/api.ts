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
}
