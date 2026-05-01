// Thin wrapper over CustomObjectsApi for the v0.2 CRDs.
// Each method maps 1:1 to a kubectl verb on a kuso CR.
//
// Lives in projects/ but used by services/, environments/, addons/v2/ too —
// these CRDs are tightly coupled and a single accessor keeps the client
// boilerplate in one place.

import { Injectable, Logger } from '@nestjs/common';
import { KubeConfig, CustomObjectsApi } from '@kubernetes/client-node';
import { KubernetesService } from '../kubernetes/kubernetes.service';
import {
  KusoAddon,
  KusoEnvironment,
  KusoProject,
  KusoService,
} from './projects.types';

const GROUP = 'application.kuso.sislelabs.com';
const VERSION = 'v1alpha1';

interface CRMetadata {
  name: string;
  namespace?: string;
  labels?: Record<string, string>;
}

@Injectable()
export class KusoResourcesService {
  private readonly logger = new Logger(KusoResourcesService.name);

  constructor(private readonly kubectl: KubernetesService) {}

  private get api(): CustomObjectsApi {
    return (this.kubectl as any).customObjectsApi as CustomObjectsApi;
  }

  private get namespace(): string {
    return process.env.KUSO_NAMESPACE || 'kuso';
  }

  // ---------------- Projects ----------------

  async listProjects(): Promise<KusoProject[]> {
    const res = await this.api.listNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoprojects',
    );
    return ((res.body as any)?.items || []) as KusoProject[];
  }

  async getProject(name: string): Promise<KusoProject | null> {
    try {
      const res = await this.api.getNamespacedCustomObject(
        GROUP,
        VERSION,
        this.namespace,
        'kusoprojects',
        name,
      );
      return res.body as KusoProject;
    } catch (e: any) {
      if (e?.response?.statusCode === 404) return null;
      throw e;
    }
  }

  async createProject(project: KusoProject): Promise<KusoProject> {
    const body = this.materialise('KusoProject', 'kusoprojects', project);
    const res = await this.api.createNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoprojects',
      body,
    );
    return res.body as KusoProject;
  }

  async deleteProject(name: string): Promise<void> {
    await this.api.deleteNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoprojects',
      name,
    );
  }

  // ---------------- Services ----------------

  async listServices(project?: string): Promise<KusoService[]> {
    const labelSelector = project
      ? `kuso.sislelabs.com/project=${project}`
      : undefined;
    const res = await this.api.listNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoservices',
      undefined,
      undefined,
      undefined,
      undefined,
      labelSelector,
    );
    return ((res.body as any)?.items || []) as KusoService[];
  }

  async getService(name: string): Promise<KusoService | null> {
    try {
      const res = await this.api.getNamespacedCustomObject(
        GROUP,
        VERSION,
        this.namespace,
        'kusoservices',
        name,
      );
      return res.body as KusoService;
    } catch (e: any) {
      if (e?.response?.statusCode === 404) return null;
      throw e;
    }
  }

  async createService(service: KusoService): Promise<KusoService> {
    const body = this.materialise('KusoService', 'kusoservices', service);
    const res = await this.api.createNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoservices',
      body,
    );
    return res.body as KusoService;
  }

  async deleteService(name: string): Promise<void> {
    await this.api.deleteNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoservices',
      name,
    );
  }

  // ---------------- Environments ----------------

  async listEnvironments(
    project?: string,
    service?: string,
  ): Promise<KusoEnvironment[]> {
    const parts: string[] = [];
    if (project) parts.push(`kuso.sislelabs.com/project=${project}`);
    if (service) parts.push(`kuso.sislelabs.com/service=${service}`);
    const labelSelector = parts.length ? parts.join(',') : undefined;
    const res = await this.api.listNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoenvironments',
      undefined,
      undefined,
      undefined,
      undefined,
      labelSelector,
    );
    return ((res.body as any)?.items || []) as KusoEnvironment[];
  }

  async getEnvironment(name: string): Promise<KusoEnvironment | null> {
    try {
      const res = await this.api.getNamespacedCustomObject(
        GROUP,
        VERSION,
        this.namespace,
        'kusoenvironments',
        name,
      );
      return res.body as KusoEnvironment;
    } catch (e: any) {
      if (e?.response?.statusCode === 404) return null;
      throw e;
    }
  }

  async createEnvironment(env: KusoEnvironment): Promise<KusoEnvironment> {
    const body = this.materialise('KusoEnvironment', 'kusoenvironments', env);
    const res = await this.api.createNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoenvironments',
      body,
    );
    return res.body as KusoEnvironment;
  }

  /**
   * Merge-patch an environment's spec. Used by the addon-secret refresh
   * path so we update existing envs in place instead of delete+create —
   * delete+create races with helm-operator's uninstall finalizer and ends
   * up stuck in "object is being deleted" when the recreate is attempted.
   */
  async patchEnvironment(
    name: string,
    patch: Record<string, any>,
  ): Promise<void> {
    await this.api.patchNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoenvironments',
      name,
      patch,
      undefined,
      undefined,
      undefined,
      { headers: { 'Content-Type': 'application/merge-patch+json' } },
    );
  }

  async deleteEnvironment(name: string): Promise<void> {
    await this.api.deleteNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoenvironments',
      name,
    );
  }

  // ---------------- Addons ----------------

  async listAddons(project?: string): Promise<KusoAddon[]> {
    const labelSelector = project
      ? `kuso.sislelabs.com/project=${project}`
      : undefined;
    const res = await this.api.listNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoaddons',
      undefined,
      undefined,
      undefined,
      undefined,
      labelSelector,
    );
    return ((res.body as any)?.items || []) as KusoAddon[];
  }

  async getAddon(name: string): Promise<KusoAddon | null> {
    try {
      const res = await this.api.getNamespacedCustomObject(
        GROUP,
        VERSION,
        this.namespace,
        'kusoaddons',
        name,
      );
      return res.body as KusoAddon;
    } catch (e: any) {
      if (e?.response?.statusCode === 404) return null;
      throw e;
    }
  }

  async createAddon(addon: KusoAddon): Promise<KusoAddon> {
    const body = this.materialise('KusoAddon', 'kusoaddons', addon);
    const res = await this.api.createNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoaddons',
      body,
    );
    return res.body as KusoAddon;
  }

  async deleteAddon(name: string): Promise<void> {
    await this.api.deleteNamespacedCustomObject(
      GROUP,
      VERSION,
      this.namespace,
      'kusoaddons',
      name,
    );
  }

  // ---------------- helpers ----------------

  private materialise<T extends { metadata: CRMetadata; spec: any }>(
    kind: string,
    _plural: string,
    body: T,
  ): T {
    return {
      apiVersion: `${GROUP}/${VERSION}`,
      kind,
      ...body,
      metadata: {
        ...body.metadata,
        namespace: body.metadata.namespace || this.namespace,
      },
    };
  }
}
