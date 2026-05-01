import { Injectable, BadRequestException, NotFoundException, Logger } from '@nestjs/common';
import { KusoResourcesService } from './kuso-resources.service';
import {
  CreateAddonDTO,
  CreateProjectDTO,
  CreateServiceDTO,
  KusoAddon,
  KusoEnvironment,
  KusoProject,
  KusoService,
} from './projects.types';

@Injectable()
export class ProjectsService {
  private readonly logger = new Logger(ProjectsService.name);

  constructor(private readonly resources: KusoResourcesService) {}

  // ---------------- projects ----------------

  async list(): Promise<KusoProject[]> {
    return this.resources.listProjects();
  }

  async describe(name: string): Promise<{
    project: KusoProject;
    services: KusoService[];
    environments: KusoEnvironment[];
    addons: KusoAddon[];
  }> {
    const project = await this.resources.getProject(name);
    if (!project) throw new NotFoundException(`project ${name} not found`);
    const [services, environments, addons] = await Promise.all([
      this.resources.listServices(name),
      this.resources.listEnvironments(name),
      this.resources.listAddons(name),
    ]);
    return { project, services, environments, addons };
  }

  async create(dto: CreateProjectDTO): Promise<KusoProject> {
    if (!dto.name) throw new BadRequestException('name is required');
    if (!dto.defaultRepo?.url) throw new BadRequestException('defaultRepo.url is required');

    const existing = await this.resources.getProject(dto.name);
    if (existing) throw new BadRequestException(`project ${dto.name} already exists`);

    const project: KusoProject = {
      metadata: { name: dto.name, labels: { 'kuso.sislelabs.com/project': dto.name } },
      spec: {
        description: dto.description,
        baseDomain: dto.baseDomain,
        defaultRepo: {
          url: dto.defaultRepo.url,
          defaultBranch: dto.defaultRepo.defaultBranch || 'main',
        },
        github: dto.github,
        previews: {
          enabled: dto.previews?.enabled ?? false,
          ttlDays: dto.previews?.ttlDays ?? 7,
        },
      },
    };
    return this.resources.createProject(project);
  }

  async delete(name: string): Promise<void> {
    const project = await this.resources.getProject(name);
    if (!project) throw new NotFoundException(`project ${name} not found`);

    // Cascade: delete envs, services, addons. Operator GC handles renderings.
    const [envs, services, addons] = await Promise.all([
      this.resources.listEnvironments(name),
      this.resources.listServices(name),
      this.resources.listAddons(name),
    ]);
    for (const e of envs) await this.resources.deleteEnvironment(e.metadata.name);
    for (const s of services) await this.resources.deleteService(s.metadata.name);
    for (const a of addons) await this.resources.deleteAddon(a.metadata.name);
    await this.resources.deleteProject(name);
  }

  // ---------------- services ----------------

  async listServices(project: string): Promise<KusoService[]> {
    return this.resources.listServices(project);
  }

  async getService(project: string, name: string): Promise<KusoService> {
    const fqn = this.svcName(project, name);
    const svc = await this.resources.getService(fqn);
    if (!svc) throw new NotFoundException(`service ${project}/${name} not found`);
    return svc;
  }

  async addService(project: string, dto: CreateServiceDTO): Promise<KusoService> {
    if (!dto.name) throw new BadRequestException('name is required');
    const proj = await this.resources.getProject(project);
    if (!proj) throw new NotFoundException(`project ${project} not found`);

    const fqn = this.svcName(project, dto.name);
    if (await this.resources.getService(fqn)) {
      throw new BadRequestException(`service ${project}/${dto.name} already exists`);
    }

    const repoUrl = dto.repo?.url || proj.spec.defaultRepo?.url || '';
    const repoPath = dto.repo?.path || '.';

    const svc: KusoService = {
      metadata: {
        name: fqn,
        labels: {
          'kuso.sislelabs.com/project': project,
          'kuso.sislelabs.com/service': dto.name,
        },
      },
      spec: {
        project,
        repo: { url: repoUrl, path: repoPath },
        runtime: dto.runtime || '',
        port: dto.port || 0,
        domains: dto.domains,
        envVars: dto.envVars || [],
        scale: dto.scale || { min: 1, max: 5, targetCPU: 70 },
        sleep: dto.sleep || { enabled: false, afterMinutes: 30 },
      },
    };
    const created = await this.resources.createService(svc);

    // Auto-create production env so the service has somewhere to run.
    // Image is left blank — first build (Phase 3 webhook flow) will populate.
    const baseDomain = proj.spec.baseDomain || `${project}.kuso.sislelabs.com`;
    const env: KusoEnvironment = {
      metadata: {
        name: `${fqn}-production`,
        labels: {
          'kuso.sislelabs.com/project': project,
          'kuso.sislelabs.com/service': dto.name,
          'kuso.sislelabs.com/env': 'production',
        },
      },
      spec: {
        project,
        service: fqn,
        kind: 'production',
        branch: proj.spec.defaultRepo?.defaultBranch || 'main',
        port: dto.port || 8080,
        replicaCount: dto.scale?.min || 1,
        host: this.defaultHost(dto.name, project, baseDomain),
        tlsEnabled: true,
        clusterIssuer: 'letsencrypt-prod',
        ingressClassName: 'traefik',
        envFromSecrets: await this.collectAddonSecrets(project),
      },
    };
    await this.resources.createEnvironment(env);

    return created;
  }

  async deleteService(project: string, name: string): Promise<void> {
    const fqn = this.svcName(project, name);
    const svc = await this.resources.getService(fqn);
    if (!svc) throw new NotFoundException(`service ${project}/${name} not found`);

    const envs = await this.resources.listEnvironments(project, fqn);
    for (const e of envs) await this.resources.deleteEnvironment(e.metadata.name);
    await this.resources.deleteService(fqn);
  }

  // ---------------- environments ----------------

  async listEnvironments(project: string): Promise<KusoEnvironment[]> {
    return this.resources.listEnvironments(project);
  }

  async getEnvironment(project: string, name: string): Promise<KusoEnvironment> {
    const fqn = this.envName(project, name);
    const env = await this.resources.getEnvironment(fqn);
    if (!env) throw new NotFoundException(`environment ${project}/${name} not found`);
    return env;
  }

  async deleteEnvironment(project: string, name: string): Promise<void> {
    const fqn = this.envName(project, name);
    const env = await this.resources.getEnvironment(fqn);
    if (!env) throw new NotFoundException(`environment ${project}/${name} not found`);
    if (env.spec.kind === 'production') {
      throw new BadRequestException('cannot delete production environment; delete the service instead');
    }
    await this.resources.deleteEnvironment(fqn);
  }

  // ---------------- addons ----------------

  async listAddons(project: string): Promise<KusoAddon[]> {
    return this.resources.listAddons(project);
  }

  async addAddon(project: string, dto: CreateAddonDTO): Promise<KusoAddon> {
    if (!dto.name || !dto.kind) {
      throw new BadRequestException('name and kind are required');
    }
    const proj = await this.resources.getProject(project);
    if (!proj) throw new NotFoundException(`project ${project} not found`);

    const fqn = this.addonName(project, dto.name);
    if (await this.resources.getAddon(fqn)) {
      throw new BadRequestException(`addon ${project}/${dto.name} already exists`);
    }

    const addon: KusoAddon = {
      metadata: {
        name: fqn,
        labels: {
          'kuso.sislelabs.com/project': project,
          'kuso.sislelabs.com/addon': dto.name,
          'kuso.sislelabs.com/addon-kind': dto.kind,
        },
      },
      spec: {
        project,
        kind: dto.kind,
        version: dto.version,
        size: dto.size || 'small',
        ha: dto.ha || false,
        storageSize: dto.storageSize,
        database: dto.database,
      },
    };
    const created = await this.resources.createAddon(addon);

    // After adding an addon, every existing environment in the project needs
    // its envFromSecrets list updated. We do this by recreating each env with
    // the updated list. Cheaper than dynamic patching and keeps the operator
    // single-shot reconciler simple.
    await this.refreshEnvironmentsAddonSecrets(project);
    return created;
  }

  async deleteAddon(project: string, name: string): Promise<void> {
    const fqn = this.addonName(project, name);
    const addon = await this.resources.getAddon(fqn);
    if (!addon) throw new NotFoundException(`addon ${project}/${name} not found`);

    await this.resources.deleteAddon(fqn);
    await this.refreshEnvironmentsAddonSecrets(project);
  }

  // ---------------- helpers ----------------

  private svcName(project: string, name: string): string {
    return `${project}-${name}`;
  }

  private envName(project: string, name: string): string {
    return name.startsWith(`${project}-`) ? name : `${project}-${name}`;
  }

  private addonName(project: string, name: string): string {
    return name.startsWith(`${project}-`) ? name : `${project}-${name}`;
  }

  private defaultHost(service: string, project: string, baseDomain: string): string {
    return `${service}.${baseDomain}`.replace(/^\.+/, '');
  }

  private async collectAddonSecrets(project: string): Promise<string[]> {
    const addons = await this.resources.listAddons(project);
    return addons.map((a) => `${project}-${this.shortAddonName(a.metadata.name, project)}-conn`);
  }

  private shortAddonName(fqn: string, project: string): string {
    return fqn.startsWith(`${project}-`) ? fqn.slice(project.length + 1) : fqn;
  }

  private async refreshEnvironmentsAddonSecrets(project: string): Promise<void> {
    const [envs, secrets] = await Promise.all([
      this.resources.listEnvironments(project),
      this.collectAddonSecrets(project),
    ]);
    for (const env of envs) {
      const next = { ...env, spec: { ...env.spec, envFromSecrets: secrets } };
      // Recreate via delete+create. Simpler than PATCH and the operator
      // re-reconciles the helm release on next sync.
      await this.resources.deleteEnvironment(env.metadata.name);
      delete (next.metadata as any).resourceVersion;
      delete (next.metadata as any).uid;
      delete (next.metadata as any).creationTimestamp;
      await this.resources.createEnvironment(next);
    }
  }
}
