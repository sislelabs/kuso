import { api } from "@/lib/api-client";

export interface GithubInstallation {
  id: number;
  account: { login: string; type: string; avatarUrl?: string };
  repos: GithubRepo[];
}

export interface GithubRepo {
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
