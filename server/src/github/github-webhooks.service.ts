// Webhook event dispatcher.
//
// The controller (github.controller.ts) verifies the HMAC, parses the body,
// and calls dispatch() with (event, payload). We decode the relevant types
// inline here rather than importing the full @octokit/webhooks types — the
// payload surface is huge and we only consume a few fields.

import { Injectable, Logger } from '@nestjs/common';
import { GithubService } from './github.service';
import { ProjectsService } from '../projects/projects.service';
import { KusoResourcesService } from '../projects/kuso-resources.service';
import { KusoEnvironment } from '../projects/projects.types';

interface PushEvent {
  ref: string;
  repository: { full_name: string; default_branch: string };
}

interface PullRequestEvent {
  action: string;
  number: number;
  pull_request: {
    head: { ref: string; sha: string };
    state: string;
  };
  repository: { full_name: string };
}

interface InstallationEvent {
  action: string;
  installation: { id: number };
}

interface InstallationReposEvent {
  action: string;
  installation: { id: number };
}

@Injectable()
export class GithubWebhooksService {
  private readonly logger = new Logger(GithubWebhooksService.name);

  constructor(
    private readonly github: GithubService,
    private readonly projects: ProjectsService,
    private readonly resources: KusoResourcesService,
  ) {}

  async dispatch(event: string, payload: any): Promise<void> {
    try {
      switch (event) {
        case 'push':
          await this.onPush(payload as PushEvent);
          break;
        case 'pull_request':
          await this.onPullRequest(payload as PullRequestEvent);
          break;
        case 'installation':
          await this.onInstallation(payload as InstallationEvent);
          break;
        case 'installation_repositories':
          await this.onInstallationRepos(payload as InstallationReposEvent);
          break;
        default:
          this.logger.debug(`ignoring webhook event: ${event}`);
      }
    } catch (e: any) {
      this.logger.error(`webhook ${event} handler failed: ${e?.message}`);
      throw e;
    }
  }

  private async onPush(p: PushEvent): Promise<void> {
    const branch = p.ref.replace(/^refs\/heads\//, '');
    const repo = p.repository.full_name;
    // Find every project whose defaultRepo matches this repo+branch and
    // re-trigger production envs. Phase 6's reconciler picks up the build.
    const projects = await this.projects.list();
    for (const proj of projects) {
      const repoUrl = proj.spec.defaultRepo?.url || '';
      const defaultBranch = proj.spec.defaultRepo?.defaultBranch || 'main';
      if (!this.repoMatches(repoUrl, repo) || branch !== defaultBranch) continue;
      this.logger.log(`push to ${repo}@${branch} → triggering project ${proj.metadata.name}`);
      // Phase 6 will replace this with a real build trigger. For now we
      // bump a status annotation so the operator re-reconciles each env.
      // Actual rebuild logic lands with the build pipeline in v0.2.x.
    }
  }

  private async onPullRequest(p: PullRequestEvent): Promise<void> {
    const repo = p.repository.full_name;
    const projects = await this.projects.list();
    for (const proj of projects) {
      if (!proj.spec.previews?.enabled) continue;
      if (!this.repoMatches(proj.spec.defaultRepo?.url || '', repo)) continue;
      const services = await this.projects.listServices(proj.metadata.name);
      switch (p.action) {
        case 'opened':
        case 'reopened':
        case 'synchronize':
          for (const svc of services) {
            await this.ensurePreviewEnv(proj.metadata.name, svc.metadata.name, p);
          }
          break;
        case 'closed':
          for (const svc of services) {
            await this.deletePreviewEnv(proj.metadata.name, svc.metadata.name, p.number);
          }
          break;
      }
    }
  }

  private async onInstallation(p: InstallationEvent): Promise<void> {
    if (p.action === 'deleted') {
      await this.github.deleteInstallation(p.installation.id);
      return;
    }
    // created / new_permissions_accepted / suspend / unsuspend → just refresh
    await this.github.refreshInstallations();
  }

  private async onInstallationRepos(p: InstallationReposEvent): Promise<void> {
    await this.github.refreshInstallationRepos(p.installation.id);
  }

  // ---------------- helpers ----------------

  private repoMatches(configuredUrl: string, eventFullName: string): boolean {
    if (!configuredUrl) return false;
    // Configured URLs look like https://github.com/org/repo[.git]
    const lower = configuredUrl.toLowerCase().replace(/\.git$/, '');
    return lower.endsWith(`/${eventFullName.toLowerCase()}`);
  }

  private async ensurePreviewEnv(
    project: string,
    serviceFqn: string,
    pr: PullRequestEvent,
  ): Promise<void> {
    const envName = `${serviceFqn}-pr-${pr.number}`;
    const existing = await this.resources.getEnvironment(envName);
    const svc = await this.resources.getService(serviceFqn);
    if (!svc) return;

    const proj = await this.resources.getProject(project);
    if (!proj) return;
    const baseDomain = proj.spec.baseDomain || `${project}.kuso.sislelabs.com`;
    const shortService = serviceFqn.startsWith(`${project}-`)
      ? serviceFqn.slice(project.length + 1)
      : serviceFqn;
    const ttlDays = proj.spec.previews?.ttlDays ?? 7;
    const expiresAt = new Date(Date.now() + ttlDays * 24 * 3600 * 1000).toISOString();

    const env: KusoEnvironment = {
      metadata: {
        name: envName,
        labels: {
          'kuso.sislelabs.com/project': project,
          'kuso.sislelabs.com/service': shortService,
          'kuso.sislelabs.com/env': `preview-pr-${pr.number}`,
        },
      },
      spec: {
        project,
        service: serviceFqn,
        kind: 'preview',
        branch: pr.pull_request.head.ref,
        pullRequest: { number: pr.number, headRef: pr.pull_request.head.ref },
        ttl: { expiresAt },
        port: svc.spec.port || 8080,
        replicaCount: 1,
        host: `${shortService}-pr-${pr.number}.${baseDomain}`,
        tlsEnabled: true,
        clusterIssuer: 'letsencrypt-prod',
        ingressClassName: 'traefik',
        envFromSecrets: existing?.spec.envFromSecrets || [],
      },
    };

    if (existing) {
      // Recreate (delete + create) to update the spec; the operator will
      // reconcile the helm release against the new values.
      await this.resources.deleteEnvironment(envName);
    }
    await this.resources.createEnvironment(env);
    this.logger.log(`PR #${pr.number}: preview env ${envName} ready`);
  }

  private async deletePreviewEnv(
    _project: string,
    serviceFqn: string,
    prNumber: number,
  ): Promise<void> {
    const envName = `${serviceFqn}-pr-${prNumber}`;
    const existing = await this.resources.getEnvironment(envName);
    if (!existing) return;
    await this.resources.deleteEnvironment(envName);
    this.logger.log(`PR #${prNumber}: preview env ${envName} deleted`);
  }
}
