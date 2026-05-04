import { api } from "@/lib/api-client";

// GithubInstallation mirrors the server's wire shape — see
// server-go/internal/http/handlers/github.go. The CLI consumes the
// same fields, so flattening account.{login,type} and renaming repos
// → repositories is what the platform uses, not what felt natural at
// the time the web client was sketched.
export interface GithubInstallation {
  id: number;
  accountLogin: string;
  accountType: string;
  accountId: number;
  repositories: GithubRepo[];
}

export interface GithubRepo {
  id: number;
  name: string;
  fullName: string;
  defaultBranch: string;
  private: boolean;
}

export interface InstallURLResponse {
  configured: boolean;
  url: string;
}

export interface DetectRuntimeResponse {
  runtime: "dockerfile" | "nixpacks" | "buildpacks" | "static";
  port: number;
  reason: string;
}

export interface AddonSuggestion {
  kind: string;
  reason: string;
}

export async function getInstallURL(): Promise<InstallURLResponse> {
  return api("/api/github/install-url");
}

export async function listInstallations(): Promise<GithubInstallation[]> {
  return api("/api/github/installations");
}

export async function listInstallationRepos(installationId: number): Promise<GithubRepo[]> {
  return api(`/api/github/installations/${installationId}/repos`);
}

export async function detectRuntime(body: {
  installationId: number;
  owner: string;
  repo: string;
  branch: string;
  path?: string;
}): Promise<DetectRuntimeResponse> {
  return api("/api/github/detect-runtime", { method: "POST", body });
}

export async function scanAddons(body: {
  installationId: number;
  owner: string;
  repo: string;
  branch: string;
  path?: string;
}): Promise<{ suggestions: AddonSuggestion[] }> {
  return api("/api/github/scan-addons", { method: "POST", body });
}

// SetupStatus reports whether the GitHub App credentials are
// configured on this kuso instance. Used by /settings/github to
// switch between "configured" and "wizard" UI.
export interface SetupStatusResponse {
  configured: boolean;
  appId?: string;
  appSlug?: string;
}

export async function getSetupStatus(): Promise<SetupStatusResponse> {
  return api("/api/github/setup-status");
}

// ConfigureBody is the payload the /settings/github wizard posts. The
// privateKey is the multi-line PEM contents of the App's downloaded
// .pem file; we accept it raw, the server canonicalizes newlines.
export interface ConfigureBody {
  appId: string;
  appSlug: string;
  clientId: string;
  clientSecret: string;
  webhookSecret: string;
  privateKey: string;
  org?: string;
}

export interface ConfigureResponse {
  saved: boolean;
  restarted: boolean;
  message: string;
}

export async function configureGithub(body: ConfigureBody): Promise<ConfigureResponse> {
  return api("/api/github/configure", { method: "POST", body });
}
