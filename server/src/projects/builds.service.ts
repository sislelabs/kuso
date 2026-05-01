// Server-side build orchestration.
//
// Lifecycle of a build:
//   1. createBuild(project, service, ref?) — resolve ref, mint token,
//      create a token Secret, create the KusoBuild CR. Operator's helm
//      chart renders a kaniko Job.
//   2. status poller (every 10s) — for each KusoBuild whose status is
//      not yet "succeeded" or "failed", look at its rendered Job. When
//      the Job completes, write its outcome into KusoBuild.status and,
//      on success, patch the matching KusoEnvironment with the new
//      image tag (which triggers the operator to roll the deployment).

import { Injectable, Logger, NotFoundException, BadRequestException } from '@nestjs/common';
import { Cron, CronExpression } from '@nestjs/schedule';
import { BatchV1Api, CoreV1Api } from '@kubernetes/client-node';
import { KubernetesService } from '../kubernetes/kubernetes.service';
import { KusoResourcesService } from './kuso-resources.service';
import { GithubService } from '../github/github.service';
import { CreateBuildRequest, KusoBuild } from './builds.types';
import { KusoService } from './projects.types';

const REGISTRY_HOST = 'kuso-registry.kuso.svc.cluster.local:5000';

@Injectable()
export class BuildsService {
  private readonly logger = new Logger(BuildsService.name);

  constructor(
    private readonly resources: KusoResourcesService,
    private readonly github: GithubService,
    private readonly kubectl: KubernetesService,
  ) {}

  // ---------------- list / get ----------------

  async list(project: string, service?: string): Promise<KusoBuild[]> {
    const fqn = service ? this.serviceFqn(project, service) : undefined;
    const builds = await this.resources.listBuilds(project, fqn);
    builds.sort((a, b) => {
      const at = a.metadata.creationTimestamp || '';
      const bt = b.metadata.creationTimestamp || '';
      return bt.localeCompare(at); // newest first
    });
    return builds;
  }

  // ---------------- create ----------------

  async createBuild(
    project: string,
    service: string,
    req: CreateBuildRequest,
  ): Promise<KusoBuild> {
    const svcCR = await this.resources.getService(this.serviceFqn(project, service));
    if (!svcCR) {
      throw new NotFoundException(`service ${project}/${service} not found`);
    }
    const proj = await this.resources.getProject(project);
    if (!proj) throw new NotFoundException(`project ${project} not found`);

    const repoUrl = svcCR.spec.repo?.url || proj.spec.defaultRepo?.url;
    if (!repoUrl) {
      throw new BadRequestException('service has no repo URL configured');
    }

    const branch =
      req.branch ||
      svcCR.spec.repo?.url
        ? proj.spec.defaultRepo?.defaultBranch || 'main'
        : 'main';

    const installationId = proj.spec.github?.installationId || 0;

    // Resolve branch -> SHA so the image tag is immutable. Fall back to
    // the literal ref if it already looks like a SHA.
    let sha = req.ref || '';
    if (!sha || !/^[0-9a-f]{40}$/.test(sha)) {
      const { owner, repo } = this.parseRepo(repoUrl);
      if (installationId > 0) {
        const resolved = await this.github.resolveBranchSha(
          installationId,
          owner,
          repo,
          branch,
        );
        if (!resolved) {
          throw new BadRequestException(
            `could not resolve ${owner}/${repo}@${branch} to a commit SHA`,
          );
        }
        sha = resolved;
      } else {
        // Public repo, no GitHub App installation — accept the branch as
        // the ref. kaniko will check out HEAD of that branch. Image tag
        // becomes the branch name with timestamp suffix to keep it unique.
        sha = `${branch}-${Date.now().toString(36)}`;
      }
    }

    const buildName = this.buildName(project, service, sha);
    const tokenSecretName = `${buildName}-token`;

    // Mint a short-lived clone token if we have an installation.
    if (installationId > 0) {
      const token = await this.github.mintInstallationToken(installationId);
      if (!token) {
        throw new BadRequestException(
          'GitHub App not configured; cannot build private repos',
        );
      }
      await this.upsertTokenSecret(tokenSecretName, token);
    }

    const imageRepo = `${REGISTRY_HOST}/${project}/${service}`;
    const build: KusoBuild = {
      metadata: {
        name: buildName,
        labels: {
          'kuso.sislelabs.com/project': project,
          'kuso.sislelabs.com/service': this.serviceFqn(project, service),
          'kuso.sislelabs.com/build-ref': sha.slice(0, 12),
        },
      },
      spec: {
        project,
        service: this.serviceFqn(project, service),
        ref: sha,
        branch,
        repo: { url: repoUrl, path: svcCR.spec.repo?.path || '.' },
        githubInstallationId: installationId || undefined,
        strategy: 'dockerfile',
        image: { repository: imageRepo, tag: this.imageTag(sha) },
      },
    };
    return this.resources.createBuild(build);
  }

  // ---------------- status poller ----------------

  // Run every 30s. Builds are minutes-long so 30s precision is fine and
  // keeps the API server load low.
  @Cron(CronExpression.EVERY_30_SECONDS, { name: 'build-status-poller' })
  async pollStatuses(): Promise<void> {
    if (process.env.KUSO_BUILD_POLLER_DISABLED === 'true') return;

    let builds: KusoBuild[];
    try {
      builds = await this.resources.listBuilds();
    } catch (e: any) {
      this.logger.debug(`listBuilds failed: ${e?.message}`);
      return;
    }
    const open = builds.filter(
      (b) => !['succeeded', 'failed'].includes(b.status?.phase || ''),
    );
    if (open.length === 0) return;

    const batchApi = (this.kubectl as any).batchV1Api as BatchV1Api;
    if (!batchApi) return;

    for (const b of open) {
      try {
        const res = await batchApi.readNamespacedJob(
          b.metadata.name,
          b.metadata.namespace || 'kuso',
        );
        const job = res.body;
        const conditions = job.status?.conditions || [];
        const succeeded = conditions.find(
          (c: any) => c.type === 'Complete' && c.status === 'True',
        );
        const failed = conditions.find(
          (c: any) => c.type === 'Failed' && c.status === 'True',
        );

        if (succeeded) {
          await this.resources.patchBuildStatus(b.metadata.name, {
            phase: 'succeeded',
            completedAt: new Date().toISOString(),
          });
          await this.promoteImageToProductionEnv(b);
          this.logger.log(`build ${b.metadata.name} succeeded → promoted`);
        } else if (failed) {
          await this.resources.patchBuildStatus(b.metadata.name, {
            phase: 'failed',
            completedAt: new Date().toISOString(),
            message: failed.message || 'job failed',
          });
          this.logger.warn(`build ${b.metadata.name} failed`);
        } else if ((job.status?.active || 0) > 0) {
          if (b.status?.phase !== 'running') {
            await this.resources.patchBuildStatus(b.metadata.name, {
              phase: 'running',
            });
          }
        }
      } catch (e: any) {
        if (e?.response?.statusCode === 404) {
          // Job hasn't been created by the operator yet — wait for next poll.
          continue;
        }
        this.logger.warn(`poll ${b.metadata.name}: ${e?.message}`);
      }
    }
  }

  // ---------------- helpers ----------------

  private async promoteImageToProductionEnv(b: KusoBuild): Promise<void> {
    const envName = `${b.spec.service}-production`;
    const env = await this.resources.getEnvironment(envName);
    if (!env) {
      this.logger.warn(
        `build ${b.metadata.name} succeeded but env ${envName} not found`,
      );
      return;
    }
    await this.resources.patchEnvironment(envName, {
      spec: {
        image: {
          repository: b.spec.image.repository,
          tag: b.spec.image.tag,
          pullPolicy: 'IfNotPresent',
        },
      },
    });
  }

  private serviceFqn(project: string, service: string): string {
    return service.startsWith(`${project}-`) ? service : `${project}-${service}`;
  }

  private buildName(project: string, service: string, sha: string): string {
    // Kubernetes resource names are 63 chars. Use the first 12 of the SHA
    // (still 4-billion-collision-resistant within a service's history).
    return `${this.serviceFqn(project, service)}-${sha.slice(0, 12)}`;
  }

  private imageTag(sha: string): string {
    return /^[0-9a-f]{40}$/.test(sha) ? sha.slice(0, 12) : sha;
  }

  private parseRepo(url: string): { owner: string; repo: string } {
    const m = url.match(/github\.com[/:](.+?)\/(.+?)(?:\.git)?$/);
    if (!m) throw new BadRequestException(`unsupported repo URL: ${url}`);
    return { owner: m[1], repo: m[2] };
  }

  private async upsertTokenSecret(name: string, token: string): Promise<void> {
    const coreApi = (this.kubectl as any).coreV1Api as CoreV1Api;
    const ns = process.env.KUSO_NAMESPACE || 'kuso';
    const body = {
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: { name, namespace: ns },
      type: 'Opaque',
      stringData: { token },
    };
    try {
      await coreApi.createNamespacedSecret(ns, body as any);
    } catch (e: any) {
      if (e?.response?.statusCode === 409) {
        // Already exists (rare — name is deterministic per build SHA).
        await coreApi.replaceNamespacedSecret(name, ns, body as any);
      } else {
        throw e;
      }
    }
  }
}
