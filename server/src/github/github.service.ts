// kuso GitHub App integration.
//
// Three responsibilities:
//   1. Mint installation-scoped Octokit clients for repo access.
//   2. Cache the list of installations + their repos in the DB so the
//      project-create flow doesn't re-query GitHub on every page load.
//   3. Auto-detect runtime + port from a repo's file tree.
//
// Configuration (env on the kuso-server pod):
//   GITHUB_APP_ID            integer, set by you after registering the App
//   GITHUB_APP_PRIVATE_KEY   PEM, set as a Secret
//   GITHUB_APP_CLIENT_ID     OAuth client id (for user sign-in)
//   GITHUB_APP_CLIENT_SECRET OAuth client secret
//   GITHUB_APP_WEBHOOK_SECRET HMAC shared secret used to verify webhooks
//   GITHUB_APP_SLUG          the App's URL slug (used to build install URLs)
//
// When the App isn't configured (env missing), every method returns an
// empty/disabled response and logs once. Lets the rest of the app boot
// in dev or before the App is registered.

import { Injectable, Logger } from '@nestjs/common';
import { App } from 'octokit';
import { PrismaClient } from '@prisma/client';
import { CachedInstallation, CachedRepo, DetectedRuntime, RepoTreeEntry } from './github.types';

@Injectable()
export class GithubService {
  private readonly logger = new Logger(GithubService.name);
  private readonly prisma = new PrismaClient();
  private app: App | null = null;
  private warnedDisabled = false;

  constructor() {
    this.tryInitApp();
  }

  /**
   * Returns true if the GitHub App env vars are wired up. The UI should
   * branch on this — show the OAuth/install flow only when ready.
   */
  isConfigured(): boolean {
    return this.app !== null;
  }

  /**
   * Public install URL the user clicks to grant the App access.
   * https://github.com/apps/<slug>/installations/new
   */
  getInstallUrl(): string | null {
    const slug = process.env.GITHUB_APP_SLUG;
    if (!slug) return null;
    return `https://github.com/apps/${slug}/installations/new`;
  }

  /**
   * OAuth authorize URL for user sign-in. The state param echoes back
   * after the round-trip; callers use it to round-trip a CSRF token.
   */
  getOAuthUrl(redirectUri: string, state: string): string | null {
    const clientId = process.env.GITHUB_APP_CLIENT_ID;
    if (!clientId) return null;
    const params = new URLSearchParams({
      client_id: clientId,
      redirect_uri: redirectUri,
      state,
      scope: 'read:user user:email',
    });
    return `https://github.com/login/oauth/authorize?${params.toString()}`;
  }

  // ---------------- installations ----------------

  async listInstallations(): Promise<CachedInstallation[]> {
    const rows = await this.prisma.githubInstallation.findMany({
      orderBy: { accountLogin: 'asc' },
    });
    return rows.map((r) => ({
      id: r.id,
      accountLogin: r.accountLogin,
      accountType: r.accountType,
      accountId: r.accountId,
      repositories: this.parseRepos(r.repositoriesJson),
    }));
  }

  async listInstallationRepos(installationId: number): Promise<CachedRepo[]> {
    const row = await this.prisma.githubInstallation.findUnique({
      where: { id: installationId },
    });
    if (!row) return [];
    return this.parseRepos(row.repositoriesJson);
  }

  /**
   * Fetch the full installation list from GitHub and replace the local
   * cache. Called on App-level events (`installation` created/deleted)
   * and as a manual refresh from the install-callback endpoint.
   */
  async refreshInstallations(): Promise<void> {
    if (!this.app) return;

    const seenIds: number[] = [];
    for await (const { installation } of this.app.eachInstallation.iterator()) {
      seenIds.push(installation.id);
      const repos = await this.fetchInstallationRepos(installation.id);
      const account = installation.account as any;
      await this.prisma.githubInstallation.upsert({
        where: { id: installation.id },
        create: {
          id: installation.id,
          accountLogin: account?.login ?? 'unknown',
          accountType: account?.type ?? 'User',
          accountId: account?.id ?? 0,
          repositoriesJson: JSON.stringify(repos),
        },
        update: {
          accountLogin: account?.login ?? 'unknown',
          accountType: account?.type ?? 'User',
          accountId: account?.id ?? 0,
          repositoriesJson: JSON.stringify(repos),
        },
      });
    }
    // Drop installations that no longer exist on GitHub.
    await this.prisma.githubInstallation.deleteMany({
      where: { id: { notIn: seenIds.length ? seenIds : [-1] } },
    });
  }

  /**
   * Refresh just one installation's repo list. Triggered from
   * `installation_repositories` webhook events.
   */
  async refreshInstallationRepos(installationId: number): Promise<void> {
    if (!this.app) return;
    const repos = await this.fetchInstallationRepos(installationId);
    await this.prisma.githubInstallation.update({
      where: { id: installationId },
      data: { repositoriesJson: JSON.stringify(repos) },
    });
  }

  /**
   * Drop a row when GitHub tells us the App was uninstalled.
   */
  async deleteInstallation(installationId: number): Promise<void> {
    await this.prisma.githubInstallation.deleteMany({
      where: { id: installationId },
    });
  }

  // ---------------- repo introspection ----------------

  /**
   * Walk a repo's tree at HEAD of a given branch. Used by the runtime
   * detector and (later) the file-browser endpoint.
   */
  async listRepoTree(
    installationId: number,
    owner: string,
    repo: string,
    branch: string,
    pathPrefix = '',
  ): Promise<RepoTreeEntry[]> {
    if (!this.app) return [];
    const octokit = await this.app.getInstallationOctokit(installationId);
    const ref = await octokit.rest.repos.getBranch({ owner, repo, branch });
    const sha = ref.data.commit.commit.tree.sha;
    const tree = await octokit.rest.git.getTree({
      owner,
      repo,
      tree_sha: sha,
      recursive: 'true',
    });
    const entries = (tree.data.tree || []).map((e) => ({
      path: e.path || '',
      type: (e.type === 'tree' ? 'tree' : 'blob') as 'blob' | 'tree',
      size: e.size,
    }));
    if (!pathPrefix) return entries;
    const prefix = pathPrefix.replace(/^\/+|\/+$/g, '') + '/';
    return entries
      .filter((e) => e.path.startsWith(prefix))
      .map((e) => ({ ...e, path: e.path.slice(prefix.length) }));
  }

  /**
   * Read a single file's contents. Falls back to '' when missing — the
   * runtime detector uses this to peek at Dockerfile/EXPOSE without
   * exploding on 404.
   */
  async readFile(
    installationId: number,
    owner: string,
    repo: string,
    branch: string,
    path: string,
  ): Promise<string> {
    if (!this.app) return '';
    const octokit = await this.app.getInstallationOctokit(installationId);
    try {
      const res = await octokit.rest.repos.getContent({
        owner,
        repo,
        ref: branch,
        path,
      });
      const data = res.data as any;
      if (typeof data?.content !== 'string') return '';
      return Buffer.from(data.content, data.encoding || 'base64').toString('utf8');
    } catch (e: any) {
      if (e?.status === 404) return '';
      this.logger.warn(`readFile ${owner}/${repo}@${branch}:${path} failed: ${e?.message}`);
      return '';
    }
  }

  /**
   * Detect runtime + port from a service's repo+path. Rules ordered by
   * priority per docs/REDESIGN.md "Auto-detect runtime".
   */
  async detectRuntime(
    installationId: number,
    owner: string,
    repo: string,
    branch: string,
    pathPrefix: string,
  ): Promise<DetectedRuntime> {
    const entries = await this.listRepoTree(installationId, owner, repo, branch, pathPrefix);
    const has = (p: string) => entries.some((e) => e.path === p);

    if (has('Dockerfile')) {
      const dockerfile = await this.readFile(
        installationId,
        owner,
        repo,
        branch,
        pathPrefix ? `${pathPrefix.replace(/\/+$/, '')}/Dockerfile` : 'Dockerfile',
      );
      const port = this.parseExpose(dockerfile) ?? 8080;
      return { runtime: 'dockerfile', port, reason: 'Dockerfile detected' };
    }

    const isStaticOnly =
      has('index.html') &&
      !has('package.json') &&
      !has('go.mod') &&
      !has('Cargo.toml') &&
      !has('requirements.txt') &&
      !has('pyproject.toml');
    if (isStaticOnly) {
      return { runtime: 'static', port: 80, reason: 'index.html only' };
    }

    if (has('package.json')) {
      const pkg = await this.readFile(
        installationId,
        owner,
        repo,
        branch,
        pathPrefix ? `${pathPrefix.replace(/\/+$/, '')}/package.json` : 'package.json',
      );
      const port = this.guessNodePort(pkg);
      return { runtime: 'nixpacks', port, reason: 'package.json detected' };
    }
    if (has('go.mod')) {
      return { runtime: 'nixpacks', port: 8080, reason: 'go.mod detected' };
    }
    if (has('Cargo.toml')) {
      return { runtime: 'nixpacks', port: 8080, reason: 'Cargo.toml detected' };
    }
    if (has('requirements.txt') || has('pyproject.toml')) {
      return { runtime: 'nixpacks', port: 8000, reason: 'Python project detected' };
    }
    return { runtime: 'unknown', port: 8080, reason: 'no recognised marker file' };
  }

  // ---------------- internals ----------------

  private tryInitApp() {
    const appId = process.env.GITHUB_APP_ID;
    const privateKey = process.env.GITHUB_APP_PRIVATE_KEY;
    if (!appId || !privateKey) {
      if (!this.warnedDisabled) {
        this.logger.warn(
          'GitHub App is not configured. Set GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY to enable repo integration.',
        );
        this.warnedDisabled = true;
      }
      return;
    }
    try {
      this.app = new App({
        appId: Number(appId),
        privateKey: privateKey.replace(/\\n/g, '\n'),
        webhooks: process.env.GITHUB_APP_WEBHOOK_SECRET
          ? { secret: process.env.GITHUB_APP_WEBHOOK_SECRET }
          : undefined,
      });
      this.logger.log('GitHub App initialised');
    } catch (e: any) {
      this.logger.error(`Failed to init GitHub App: ${e?.message}`);
    }
  }

  private async fetchInstallationRepos(installationId: number): Promise<CachedRepo[]> {
    if (!this.app) return [];
    const octokit = await this.app.getInstallationOctokit(installationId);
    const out: CachedRepo[] = [];
    let page = 1;
    while (true) {
      const res = await octokit.rest.apps.listReposAccessibleToInstallation({
        per_page: 100,
        page,
      });
      for (const r of res.data.repositories) {
        out.push({
          id: r.id,
          name: r.name,
          fullName: r.full_name,
          private: r.private,
          defaultBranch: r.default_branch || 'main',
        });
      }
      if (res.data.repositories.length < 100) break;
      page += 1;
    }
    return out;
  }

  private parseRepos(json: string): CachedRepo[] {
    try {
      return JSON.parse(json) as CachedRepo[];
    } catch {
      return [];
    }
  }

  private parseExpose(dockerfile: string): number | null {
    const match = dockerfile.match(/^\s*EXPOSE\s+(\d+)/im);
    if (!match) return null;
    const n = Number(match[1]);
    return Number.isFinite(n) ? n : null;
  }

  private guessNodePort(pkgJson: string): number {
    try {
      const pkg = JSON.parse(pkgJson);
      const deps = { ...pkg.dependencies, ...pkg.devDependencies };
      if (deps?.next) return 3000; // Next.js
      if (deps?.vite) return 5173; // Vite dev (production usually static)
      if (deps?.nuxt || deps?.['@nuxt/core']) return 3000;
      if (deps?.fastify) return 3000;
    } catch {
      // ignore
    }
    // Conservative default: 3000 — most node web frameworks use it.
    return 3000;
  }
}
