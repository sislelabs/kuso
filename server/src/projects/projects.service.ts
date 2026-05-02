import {
  Injectable,
  BadRequestException,
  NotFoundException,
  Logger,
} from '@nestjs/common';
import { CoreV1Api } from '@kubernetes/client-node';
import { KubernetesService } from '../kubernetes/kubernetes.service';
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

  constructor(
    private readonly resources: KusoResourcesService,
    private readonly kubectl: KubernetesService,
  ) {}

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
    if (!dto.defaultRepo?.url)
      throw new BadRequestException('defaultRepo.url is required');

    const existing = await this.resources.getProject(dto.name);
    if (existing)
      throw new BadRequestException(`project ${dto.name} already exists`);

    const project: KusoProject = {
      metadata: {
        name: dto.name,
        labels: { 'kuso.sislelabs.com/project': dto.name },
      },
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
    for (const e of envs)
      await this.resources.deleteEnvironment(e.metadata.name);
    for (const s of services)
      await this.resources.deleteService(s.metadata.name);
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
    if (!svc)
      throw new NotFoundException(`service ${project}/${name} not found`);
    return svc;
  }

  async addService(
    project: string,
    dto: CreateServiceDTO,
  ): Promise<KusoService> {
    if (!dto.name) throw new BadRequestException('name is required');
    const proj = await this.resources.getProject(project);
    if (!proj) throw new NotFoundException(`project ${project} not found`);

    const fqn = this.svcName(project, dto.name);
    if (await this.resources.getService(fqn)) {
      throw new BadRequestException(
        `service ${project}/${dto.name} already exists`,
      );
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

  // ---------------- secrets ----------------

  /**
   * Lists the keys (NOT values) of a service's secrets. Secrets are
   * Kubernetes Secrets mounted into the running pod via envFromSecrets.
   *
   * Two scopes:
   *   - shared: <project>-<service>-secrets, mounted on EVERY env of
   *             the service (env=undefined or env="*").
   *   - per-env: <project>-<service>-<env>-secrets, mounted only on
   *              that specific env. Per-env values OVERRIDE shared
   *              (envFrom mounts shared first, per-env second).
   *
   * When env is undefined, this returns ONLY the shared keys. To list
   * per-env keys, pass the env name. To list the merged effective set,
   * call listSecretKeysEffective.
   */
  async listSecretKeys(
    project: string,
    service: string,
    env?: string,
  ): Promise<string[]> {
    await this.getService(project, service); // 404s if missing
    const secret = await this.readSecret(
      this.secretName(project, service, env),
    );
    return secret ? Object.keys(secret) : [];
  }

  async setSecret(
    project: string,
    service: string,
    key: string,
    value: string,
    env?: string,
  ): Promise<void> {
    if (!key) throw new BadRequestException('key is required');
    await this.getService(project, service);
    if (env) await this.assertEnvExists(project, service, env);
    const name = this.secretName(project, service, env);
    const current = (await this.readSecret(name)) || {};
    current[key] = value;
    await this.writeSecret(name, current);
    if (env) {
      await this.attachSecretToEnvironment(project, service, env, name);
      await this.bumpSecretsRev(project, service, env);
    } else {
      await this.attachSecretToEnvironments(project, service, name);
      await this.bumpSecretsRev(project, service);
    }
  }

  async unsetSecret(
    project: string,
    service: string,
    key: string,
    env?: string,
  ): Promise<void> {
    await this.getService(project, service);
    const name = this.secretName(project, service, env);
    const current = (await this.readSecret(name)) || {};
    if (!(key in current)) {
      throw new NotFoundException(`secret key ${key} not found`);
    }
    delete current[key];
    if (Object.keys(current).length === 0) {
      await this.deleteSecret(name);
      // Also drop the envFromSecrets reference from the service's envs
      // so the next reconcile doesn't keep mounting an empty Secret.
      if (env) {
        await this.detachSecretFromEnvironment(project, service, env, name);
      } else {
        await this.detachSecretFromEnvironments(project, service, name);
      }
    } else {
      await this.writeSecret(name, current);
    }
    if (env) {
      await this.bumpSecretsRev(project, service, env);
    } else {
      await this.bumpSecretsRev(project, service);
    }
  }

  /**
   * Bump spec.secretsRev on the affected env CR(s) so helm-operator
   * re-renders the Deployment with a new pod-template annotation, which
   * triggers a rolling restart. Without this, value-only updates to
   * existing envFrom Secrets are invisible to running pods.
   *
   * env: undefined -> bump every env of the service (shared scope).
   *      defined   -> bump just that one env.
   */
  private async bumpSecretsRev(
    project: string,
    service: string,
    env?: string,
  ): Promise<void> {
    const rev = String(Date.now());
    if (env) {
      const target = await this.findEnv(project, service, env);
      await this.resources.patchEnvironment(target.metadata.name, {
        spec: { secretsRev: rev },
      });
      return;
    }
    const envs = await this.envsForService(project, service);
    for (const e of envs) {
      await this.resources.patchEnvironment(e.metadata.name, {
        spec: { secretsRev: rev },
      });
    }
  }

  /**
   * Build a per-scope Secret name. env="" or undefined -> shared.
   * Otherwise the env name is sanitised (lowercase, kebab) and appended.
   */
  private secretName(project: string, service: string, env?: string): string {
    const base = this.svcName(project, service);
    if (!env) return `${base}-secrets`;
    const safe = env.toLowerCase().replace(/[^a-z0-9-]/g, '-');
    return `${base}-${safe}-secrets`;
  }

  private async assertEnvExists(
    project: string,
    service: string,
    env: string,
  ): Promise<void> {
    const fqn = this.svcName(project, service);
    const envName = env.includes('-') ? env : `${fqn}-${env}`;
    const found = await this.resources.getEnvironment(envName);
    if (!found) {
      throw new NotFoundException(
        `environment ${envName} not found for ${project}/${service}`,
      );
    }
  }

  private async readSecret(
    name: string,
  ): Promise<Record<string, string> | null> {
    const coreApi = (this.kubectl as any).coreV1Api as CoreV1Api;
    try {
      const res = await coreApi.readNamespacedSecret(
        name,
        process.env.KUSO_NAMESPACE || 'kuso',
      );
      const data = res.body.data || {};
      const out: Record<string, string> = {};
      for (const [k, v] of Object.entries(data)) {
        out[k] = Buffer.from(v as string, 'base64').toString('utf8');
      }
      return out;
    } catch (e: any) {
      if (e?.response?.statusCode === 404) return null;
      throw e;
    }
  }

  private async writeSecret(
    name: string,
    data: Record<string, string>,
  ): Promise<void> {
    const coreApi = (this.kubectl as any).coreV1Api as CoreV1Api;
    const ns = process.env.KUSO_NAMESPACE || 'kuso';
    const body: any = {
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: { name, namespace: ns },
      type: 'Opaque',
      stringData: data,
    };
    try {
      await coreApi.createNamespacedSecret(ns, body);
    } catch (e: any) {
      if (e?.response?.statusCode === 409) {
        await coreApi.replaceNamespacedSecret(name, ns, body);
      } else {
        throw e;
      }
    }
  }

  private async deleteSecret(name: string): Promise<void> {
    const coreApi = (this.kubectl as any).coreV1Api as CoreV1Api;
    try {
      await coreApi.deleteNamespacedSecret(
        name,
        process.env.KUSO_NAMESPACE || 'kuso',
      );
    } catch (e: any) {
      if (e?.response?.statusCode !== 404) throw e;
    }
  }

  private async attachSecretToEnvironments(
    project: string,
    service: string,
    secretName: string,
  ): Promise<void> {
    const envs = await this.envsForService(project, service);
    for (const env of envs) {
      const existing = (env.spec.envFromSecrets || []) as string[];
      if (existing.includes(secretName)) continue;
      const next = [...existing, secretName];
      await this.resources.patchEnvironment(env.metadata.name, {
        spec: { envFromSecrets: next },
      });
    }
  }

  private async detachSecretFromEnvironments(
    project: string,
    service: string,
    secretName: string,
  ): Promise<void> {
    const envs = await this.envsForService(project, service);
    for (const env of envs) {
      const existing = (env.spec.envFromSecrets || []) as string[];
      if (!existing.includes(secretName)) continue;
      const next = existing.filter((s) => s !== secretName);
      await this.resources.patchEnvironment(env.metadata.name, {
        spec: { envFromSecrets: next },
      });
    }
  }

  // env may be either the short env name ("production", "preview-pr-42")
  // or the fully-qualified KusoEnvironment name ("hello-web-production").
  private async attachSecretToEnvironment(
    project: string,
    service: string,
    env: string,
    secretName: string,
  ): Promise<void> {
    const target = await this.findEnv(project, service, env);
    const existing = (target.spec.envFromSecrets || []) as string[];
    if (existing.includes(secretName)) return;
    await this.resources.patchEnvironment(target.metadata.name, {
      spec: { envFromSecrets: [...existing, secretName] },
    });
  }

  private async detachSecretFromEnvironment(
    project: string,
    service: string,
    env: string,
    secretName: string,
  ): Promise<void> {
    const target = await this.findEnv(project, service, env);
    const existing = (target.spec.envFromSecrets || []) as string[];
    if (!existing.includes(secretName)) return;
    await this.resources.patchEnvironment(target.metadata.name, {
      spec: { envFromSecrets: existing.filter((s) => s !== secretName) },
    });
  }

  private async findEnv(
    project: string,
    service: string,
    env: string,
  ): Promise<KusoEnvironment> {
    const envs = await this.envsForService(project, service);
    const fqn = this.svcName(project, service);
    const target = envs.find((e) => {
      const n = e.metadata.name;
      return n === env || n === `${fqn}-${env}`;
    });
    if (!target) {
      throw new NotFoundException(
        `environment ${env} not found for ${project}/${service}`,
      );
    }
    return target;
  }

  /**
   * Find every KusoEnvironment for a given (project, service). Filters
   * project-wide and matches on spec.service. We can't rely on the
   * `kuso.sislelabs.com/service` label because some envs label with the
   * short name ("web") and others with the fqn ("hello-web") — this
   * looks at the spec instead, which is canonical.
   */
  private async envsForService(
    project: string,
    service: string,
  ): Promise<KusoEnvironment[]> {
    const fqn = this.svcName(project, service);
    const all = await this.resources.listEnvironments(project);
    return all.filter((e) => {
      const s = e.spec.service;
      return s === fqn || s === service;
    });
  }

  // ---------------- env vars ----------------

  /**
   * Returns the service's plain env vars (key=value pairs) AND the keys
   * of any secret-typed env vars (without values). Secret values are
   * never sent over the wire — to inspect them, the user must read the
   * underlying Secret directly via kubectl.
   */
  async getEnv(
    project: string,
    name: string,
  ): Promise<{
    plain: { name: string; value: string }[];
    secretKeys: string[];
  }> {
    const svc = await this.getService(project, name);
    const envVars = (svc.spec.envVars || []) as any[];
    const plain: { name: string; value: string }[] = [];
    const secretKeys: string[] = [];
    for (const e of envVars) {
      if (e?.valueFrom?.secretKeyRef) {
        secretKeys.push(String(e.name));
      } else {
        plain.push({ name: String(e.name), value: String(e.value ?? '') });
      }
    }
    return { plain, secretKeys };
  }

  /**
   * Replace the service's env var list. Pass-through to the CR; helm
   * re-renders the Deployment on the next env reconcile. Caller decides
   * whether each entry is plain {name,value} or secretKeyRef-shaped.
   */
  async setEnv(project: string, name: string, envVars: any[]): Promise<void> {
    const fqn = this.svcName(project, name);
    if (!(await this.resources.getService(fqn))) {
      throw new NotFoundException(`service ${project}/${name} not found`);
    }
    await this.resources.patchService(fqn, { spec: { envVars } });
    // Also patch every environment of this service so existing
    // KusoEnvironments pick up the new envVars (the env's spec.envVars
    // list is server-merged on top of the service's).
    const envs = await this.resources.listEnvironments(project, fqn);
    for (const env of envs) {
      await this.resources.patchEnvironment(env.metadata.name, {
        spec: { envVars },
      });
    }
  }

  async deleteService(project: string, name: string): Promise<void> {
    const fqn = this.svcName(project, name);
    const svc = await this.resources.getService(fqn);
    if (!svc)
      throw new NotFoundException(`service ${project}/${name} not found`);

    const envs = await this.resources.listEnvironments(project, fqn);
    for (const e of envs)
      await this.resources.deleteEnvironment(e.metadata.name);
    await this.resources.deleteService(fqn);
  }

  // ---------------- environments ----------------

  async listEnvironments(project: string): Promise<KusoEnvironment[]> {
    return this.resources.listEnvironments(project);
  }

  async getEnvironment(
    project: string,
    name: string,
  ): Promise<KusoEnvironment> {
    const fqn = this.envName(project, name);
    const env = await this.resources.getEnvironment(fqn);
    if (!env)
      throw new NotFoundException(`environment ${project}/${name} not found`);
    return env;
  }

  async deleteEnvironment(project: string, name: string): Promise<void> {
    const fqn = this.envName(project, name);
    const env = await this.resources.getEnvironment(fqn);
    if (!env)
      throw new NotFoundException(`environment ${project}/${name} not found`);
    if (env.spec.kind === 'production') {
      throw new BadRequestException(
        'cannot delete production environment; delete the service instead',
      );
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
      throw new BadRequestException(
        `addon ${project}/${dto.name} already exists`,
      );
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
    if (!addon)
      throw new NotFoundException(`addon ${project}/${name} not found`);

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

  private defaultHost(
    service: string,
    project: string,
    baseDomain: string,
  ): string {
    return `${service}.${baseDomain}`.replace(/^\.+/, '');
  }

  private async collectAddonSecrets(project: string): Promise<string[]> {
    // Secret name convention agreed with kusoaddon helm chart's
    // connSecretName template: `<addon-CR-name>-conn`. The addon CR is
    // already named `<project>-<short>` by addonName(), so the rendered
    // secret ends up like `smoke-pg-conn` for project=smoke addon=pg.
    const addons = await this.resources.listAddons(project);
    return addons.map((a) => `${a.metadata.name}-conn`);
  }

  private shortAddonName(fqn: string, project: string): string {
    return fqn.startsWith(`${project}-`) ? fqn.slice(project.length + 1) : fqn;
  }

  private async refreshEnvironmentsAddonSecrets(
    project: string,
  ): Promise<void> {
    const [envs, secrets] = await Promise.all([
      this.resources.listEnvironments(project),
      this.collectAddonSecrets(project),
    ]);
    // Merge-patch each env's spec.envFromSecrets. Earlier we did delete +
    // create, but that races with helm-operator's uninstall finalizer:
    // delete blocks on the helm uninstall, the create lands in
    // "object is being deleted", and the env never recovers. PATCH is
    // also the right semantic — we're updating one field, not replacing
    // the resource — and the operator re-reconciles on spec change.
    for (const env of envs) {
      await this.resources.patchEnvironment(env.metadata.name, {
        spec: { envFromSecrets: secrets },
      });
    }
  }
}
