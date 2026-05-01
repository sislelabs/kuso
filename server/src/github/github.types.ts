// Wire shapes for the kuso GitHub App integration.
//
// We intentionally don't re-export Octokit's webhook event types here —
// those are huge and we only consume a small subset. The handlers in
// github.controller.ts narrow the payload to what we need.

export interface CachedRepo {
  id: number;
  name: string;
  fullName: string;
  private: boolean;
  defaultBranch: string;
}

export interface CachedInstallation {
  id: number;
  accountLogin: string;
  accountType: string;
  accountId: number;
  repositories: CachedRepo[];
}

export interface RepoTreeEntry {
  path: string;
  type: 'blob' | 'tree';
  size?: number;
}

export interface DetectedRuntime {
  runtime: 'dockerfile' | 'nixpacks' | 'buildpacks' | 'static' | 'unknown';
  port: number;
  reason: string;
}
