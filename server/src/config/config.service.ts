import { Injectable, Logger } from '@nestjs/common';
import { Runpack as DBRunpack, PrismaClient } from '@prisma/client';
import { readFileSync, writeFileSync } from 'fs';
import { join } from 'path';
import * as YAML from 'yaml';
import { KubernetesService } from '../kubernetes/kubernetes.service';
import { INotification } from '../notifications/notifications.interface';
import { NotificationsService } from '../notifications/notifications.service';
import { Runpack } from './buildpack/runpack';
import { IKusoCRD, IKusoConfig, IRegistry } from './config.interface';
import { KusoConfig } from './kuso-config/kuso-config';
import { PodSize } from './podsize/podsize';

import * as dotenv from 'dotenv';
dotenv.config();

@Injectable()
export class ConfigService {
  private readonly logger = new Logger(ConfigService.name);
  private readonly prisma = new PrismaClient();
  // runningConfig is populated asynchronously in reloadRunningConfig() from
  // the cluster-wide Kuso CR. Initialise to a safe empty default so methods
  // like getBanner() / getTemplateConfig() don't NPE before the CR is loaded
  // (or when no CR exists yet, e.g. on a fresh install where the operator
  // didn't deploy the server itself).
  private runningConfig: IKusoConfig = {
    podSizeList: [],
    buildpacks: [],
    clusterissuer: process.env.KUSO_CLUSTER_ISSUER || 'letsencrypt-prod',
    notifications: [],
    templates: {
      enabled: process.env.KUSO_TEMPLATES_ENABLED !== 'false',
      // Default catalog points at the upstream Kubero template index until
      // sislelabs/kuso-templates is mirrored. The operator-managed Kuso CR
      // can override this once present (see reloadRunningConfig).
      catalogs: [
        {
          name: 'Kuso Apps',
          description: 'Community-maintained app templates',
          index: {
            url: 'https://raw.githubusercontent.com/kubero-dev/templates/main/index.json',
            format: 'json',
          },
        },
        {
          name: 'Kuso Frameworks',
          description: 'Framework starters',
          index: {
            url: 'https://raw.githubusercontent.com/kubero-dev/templates/main/index-frameworks.json',
            format: 'json',
          },
        },
      ] as any,
    },
    kuso: {
      console: { enabled: false },
      admin: { disabled: false },
      readonly: false,
      banner: { message: '', bgcolor: 'white', fontcolor: 'white', show: false },
    } as any,
  } as IKusoConfig;
  private features: { [key: string]: boolean } = {
    sleep: false,
    metrics: false,
    /* suggested features
        console: false,
        logs: false,
        audit: false,
        notifications: false,
        templates: false,
        addons: false,
        deployments: false,
        security: false,
        settings: false,
        */
  };

  constructor(
    private readonly kubectl: KubernetesService,
    private notification: NotificationsService,
  ) {
    this.reloadRunningConfig();
    this.runFeatureCheck();
    this.setKusoUIVersion();
  }

  private setKusoUIVersion() {
    if (process.env.npm_package_version == undefined) {
      const fs = require('fs');
      const path = require('path');
      const filePath = path.resolve(__dirname, '../VERSION');
      if (!fs.existsSync(filePath)) {
        process.env.npm_package_version = 'no version';
      } else {
        process.env.npm_package_version = fs.readFileSync(filePath, 'utf8');
      }
    }

    this.logger.debug('Kuso UI Version: ' + process.env.npm_package_version);
  }
  // Load settings from a file or from kubernetes
  async getSettings(): Promise<KusoConfig> {
    if (this.checkAdminDisabled()) {
      return new KusoConfig(new Object() as IKusoConfig);
    }

    // TODO: This might fail with a local filesystem config
    const config: any = {};
    const namespace = process.env.KUSO_NAMESPACE || 'kuso';
    const kusoes = await this.kubectl.getKusoConfig(namespace);
    config.settings = kusoes.spec;
    /*
        const kusoconfig = await this.readConfig()
        config.settings = new KusoConfig(kusoconfig)
        */

    config['secrets'] = {
      GITHUB_BASEURL: process.env.GITHUB_BASEURL || '',
      GITHUB_PERSONAL_ACCESS_TOKEN:
        process.env.GITHUB_PERSONAL_ACCESS_TOKEN || '',
      GITEA_PERSONAL_ACCESS_TOKEN:
        process.env.GITEA_PERSONAL_ACCESS_TOKEN || '',
      GITEA_BASEURL: process.env.GITEA_BASEURL || '',
      GITLAB_PERSONAL_ACCESS_TOKEN:
        process.env.GITLAB_PERSONAL_ACCESS_TOKEN || '',
      GITLAB_BASEURL: process.env.GITLAB_BASEURL || '',
      BITBUCKET_APP_PASSWORD: process.env.BITBUCKET_APP_PASSWORD || '',
      BITBUCKET_USERNAME: process.env.BITBUCKET_USERNAME || '',
      GOGS_PERSONAL_ACCESS_TOKEN: process.env.GOGS_PERSONAL_ACCESS_TOKEN || '',
      GOGS_BASEURL: process.env.GOGS_BASEURL || '',
      KUSO_WEBHOOK_SECRET: process.env.KUSO_WEBHOOK_SECRET || '',
      GITHUB_CLIENT_SECRET: process.env.GITHUB_CLIENT_SECRET || '',
      OAUTH2_CLIENT_SECRET: process.env.OAUTH2_CLIENT_SECRET || '',
    };
    return config;
  }

  public async updateSettings(config: any): Promise<KusoConfig> {
    if (this.checkAdminDisabled()) {
      return new KusoConfig({} as IKusoConfig);
    }

    const namespace = process.env.KUSO_NAMESPACE || 'kuso';
    const kusoes = await this.kubectl.getKusoConfig(namespace);
    kusoes.spec = config.settings;

    // Write local config file in dev mode
    if (process.env.NODE_ENV != 'production') {
      console.log('DEV MODE: write local config');
      this.writeConfig(kusoes.spec.kuso.config);
    }

    await this.kubectl.updateKusoConfig(namespace, kusoes);
    await this.kubectl.updateKusoSecret(namespace, config.secrets);
    this.setSecretEnv(config.secrets);

    const m = {
      name: 'updateSettings',
      user: '',
      resource: 'system',
      action: 'update',
      severity: 'normal',
      message: 'Kuso settings updated',
      pipelineName: '',
      phaseName: '',
      appName: '',
      data: {},
    } as INotification;
    await this.notification.send(m);

    return kusoes;
  }

  private setSecretEnv(secrets: any) {
    /*
    for (const key in secrets) {
        process.env[key] = secrets[key]
    }
    */
    process.env.GITHUB_BASEURL = secrets.GITHUB_BASEURL;
    process.env.GITHUB_PERSONAL_ACCESS_TOKEN =
      secrets.GITHUB_PERSONAL_ACCESS_TOKEN;
    process.env.GITEA_PERSONAL_ACCESS_TOKEN =
      secrets.GITEA_PERSONAL_ACCESS_TOKEN;
    process.env.GITEA_BASEURL = secrets.GITEA_BASEURL;
    process.env.GITLAB_PERSONAL_ACCESS_TOKEN =
      secrets.GITLAB_PERSONAL_ACCESS_TOKEN;
    process.env.GITLAB_BASEURL = secrets.GITLAB_BASEURL;
    process.env.BITBUCKET_APP_PASSWORD = secrets.BITBUCKET_APP_PASSWORD;
    process.env.BITBUCKET_USERNAME = secrets.BITBUCKET_USERNAME;
    process.env.GOGS_PERSONAL_ACCESS_TOKEN = secrets.GOGS_PERSONAL_ACCESS_TOKEN;
    process.env.GOGS_BASEURL = secrets.GOGS_BASEURL;
    process.env.KUSO_WEBHOOK_SECRET = secrets.KUSO_WEBHOOK_SECRET;
    process.env.GITHUB_CLIENT_SECRET = secrets.GITHUB_CLIENT_SECRET;
    process.env.OAUTH2_CLIENT_SECRET = secrets.OAUTH2_CLIENT_SECRET;
  }

  private setEnvVar(key: string, value: string): void {
    if (process.env[key] == undefined || process.env[key] == '') {
      // Only set the environment variable if it is not already set or empty
      process.env[key] = value;
      //this.logger.warn(`DEPRECATED v3.x.0: Environment variable ${key} set to ${value}. Use configmap instead.`);
    }
  }

  private loadDeprecatedVarsToEnv(config: IKusoConfig): void {
    // Update environment variables based on the config
    this.setEnvVar(
      'KUSO_READONLY',
      config.kuso?.readonly ? 'true' : 'false',
    );
    this.setEnvVar(
      'KUSO_CONSOLE_ENABLED',
      config.kuso?.console?.enabled ? 'true' : 'false',
    );
    this.setEnvVar(
      'KUSO_ADMIN_DISABLED',
      config.kuso?.admin?.disabled ? 'true' : 'false',
    );
    this.setEnvVar(
      'KUSO_BANNER_SHOW',
      config.kuso?.banner?.show ? 'true' : 'false',
    );
    this.setEnvVar(
      'KUSO_BANNER_MESSAGE',
      config.kuso?.banner?.message || 'Welcome to Kuso!',
    );
    this.setEnvVar(
      'KUSO_BANNER_BGCOLOR',
      config.kuso?.banner?.bgcolor || '#8560a963',
    );
    this.setEnvVar(
      'KUSO_BANNER_FONTCOLOR',
      config.kuso?.banner?.fontcolor || '#00000087',
    );
    this.setEnvVar(
      'KUSO_TEMPLATES_ENABLED',
      config.templates?.enabled ? 'true' : 'false',
    );
    this.setEnvVar(
      'KUSO_CLUSTER_ISSUER',
      config.clusterissuer || 'letsencrypt-prod',
    );
  }

  private reloadRunningConfig(): void {
    this.readConfig()
      .then((config) => {
        this.logger.debug('Kuso config loaded');
        this.runningConfig = config;
        this.loadDeprecatedVarsToEnv(config);
      })
      .catch((error) => {
        this.logger.error('Error reading kusoes config');
        this.logger.error(error);
      });
  }

  private async readConfig(): Promise<IKusoConfig> {
    if (process.env.NODE_ENV === 'production') {
      const kusoCRD = await this.readConfigFromKubernetes();
      this.logger.debug('Kuso config loaded from Kubernetes');
      return kusoCRD.kuso.config;
    } else {
      this.logger.debug('Kuso config loaded from filesystem (dev mode)');
      return this.readConfigFromFS();
    }
  }

  private async readConfigFromKubernetes(): Promise<IKusoCRD> {
    const namespace = process.env.KUSO_NAMESPACE || 'kuso';
    const kusoes = await this.kubectl.getKusoConfig(namespace);
    if (!kusoes || !kusoes.spec) {
      this.logger.error('Kuso config not found in Kubernetes');
      throw new Error('Kuso config not found');
    }
    return kusoes.spec;
  }

  private readConfigFromFS(): IKusoConfig {
    // read config from local filesystem (dev mode)
    let configPath: string;
    if (process.env.KUSO_CONFIG_PATH) {
      configPath = process.env.KUSO_CONFIG_PATH;
    } else {
      // In development, look in the project root; in production, look in dist
      const isProduction = process.env.NODE_ENV === 'production';
      configPath = isProduction
        ? join(__dirname, 'config.yaml')
        : join(process.cwd(), 'config.yaml');
    }

    let settings: string;
    try {
      settings = readFileSync(configPath, 'utf8');
      return YAML.parse(settings) as IKusoConfig;
    } catch (_error) {
      this.logger.error('Error reading config file');
      this.logger.error(`Attempted path: ${configPath}`);
      this.logger.error(_error);

      return new Object() as IKusoConfig;
    }
  }

  // write config to local filesystem (dev mode)
  private writeConfig(configMap: KusoConfig) {
    let configPath: string;
    if (process.env.KUSO_CONFIG_PATH) {
      configPath = process.env.KUSO_CONFIG_PATH;
    } else {
      // In development, write to project root; in production, write to dist
      const isProduction = process.env.NODE_ENV === 'production';
      configPath = isProduction
        ? join(__dirname, 'config.yaml')
        : join(process.cwd(), 'config.yaml');
    }

    writeFileSync(configPath, YAML.stringify(configMap), {
      flag: 'w',
      encoding: 'utf8',
    });
  }

  public async getDefaultRegistry(): Promise<any> {
    let registry = process.env.KUSO_REGISTRY || {
      account: {
        hash: '$2y$05$czQZpvtDYc5OzM/1r1pH0eAplT/okohh/mXoWl/Y65ZP/8/jnSWZq',
        password: 'kuso',
        username: 'kuso',
      },
      create: false,
      enabled: false,
      host: 'registry.demo.kuso.sislelabs.com',
      port: 443,
      storage: '1Gi',
      storageClassName: null,
      subpath: '',
    };
    try {
      const namespace = process.env.KUSO_NAMESPACE || 'kuso';
      const kusoes = await this.kubectl.getKusoConfig(namespace);
      registry = kusoes.spec.registry;
    } catch (_error) {
      this.logger.error('Error getting kusoes config');
    }
    return registry;
  }

  public getBanner() {
    const defaultbanner = {
      show: false,
      text: '',
      bgcolor: 'white',
      fontcolor: 'white',
    };

    const banner = this.runningConfig.kuso?.banner || defaultbanner;
    return banner;
  }

  public checkAdminDisabled(): boolean {
    if (process.env.KUSO_ADMIN_DISABLED === 'true') {
      this.logger.warn('Admin is disabled');
      return true;
    }
    return false;
  }

  public async validateKubeconfig(
    kubeConfig: string,
    kubeContext: string,
  ): Promise<any> {
    if (process.env.KUSO_SETUP != 'enabled') {
      return {
        error: 'Setup is disabled. Set env KUSO_SETUP=enabled and retry',
        status: 'error',
      };
    }
    return this.kubectl.validateKubeconfig(kubeConfig, kubeContext);
  }

  public async updateRunningConfig(
    kubeConfig: string,
    kubeContext: string,
    kusoNamespace: string,
    KusoSessionKey: string,
    kusoWebhookSecret: string,
  ): Promise<{ error: string; status: string }> {
    if (process.env.KUSO_SETUP != 'enabled') {
      return {
        error: 'Setup is disabled. Set env KUSO_SETUP=enabled and retry',
        status: 'error',
      };
    }

    process.env.KUSO_CONTEXT = kubeContext;
    process.env.KUSO_NAMESPACE = kusoNamespace;
    process.env.KUSO_SESSION_KEY = KusoSessionKey;
    process.env.KUBECONFIG_BASE64 = kubeConfig;
    process.env.KUSO_SETUP = 'disabled';

    this.kubectl.updateKubectlConfig(kubeConfig, kubeContext);

    await this.kubectl.createNamespace(kusoNamespace);
    return {
      error: '',
      status: 'ok',
    };
  }

  public async checkComponent(component: string): Promise<any> {
    const ret = {
      //reason : "Component not found",
      status: 'error',
    };

    if (component === 'operator') {
      //let operator = await this.kubectl.checkCustomResourceDefinition("kusoes.application.kuso.sislelabs.com")
      const operator = await this.kubectl.checkNamespace(
        'kuso-operator-system',
      );
      if (operator) {
        ret.status = 'ok';
      }
    }

    if (component === 'metrics') {
      const metrics = await this.kubectl.checkDeployment(
        'kube-system',
        'metrics-server',
      );
      if (metrics) {
        ret.status = 'ok';
      }
    }

    if (component === 'debug') {
      const metrics = await this.kubectl.checkNamespace('default');
      if (metrics) {
        ret.status = 'ok';
      }
    }

    if (component === 'ingress') {
      const ingress = await this.kubectl.checkNamespace('ingress-nginx');
      if (ingress) {
        ret.status = 'ok';
      }
    }

    return ret;
  }

  getBuildpipelineEnabled() {
    return process.env.KUSO_BUILD_REGISTRY
      ? process.env.KUSO_BUILD_REGISTRY != undefined
      : false;
  }

  getTemplateEnabled() {
    if (process.env.KUSO_TEMPLATES_ENABLED == undefined) {
      return false;
    }
    if (process.env.KUSO_TEMPLATES_ENABLED == 'true') {
      return true;
    }
    return false;
  }

  public getTemplateConfig() {
    return this.runningConfig.templates;
  }

  getConsoleEnabled(): boolean {
    if (process.env.KUSO_CONSOLE_ENABLED == undefined) {
      this.logger.warn(
        'KUSO_CONSOLE_ENABLED is not set, defaulting to false',
      );
      return false;
    }
    if (process.env.KUSO_CONSOLE_ENABLED == 'true') {
      return true;
    }
    return false;
  }

  setMetricsStatus(status: boolean) {
    this.features.metrics = status;
  }

  getMetricsEnabled(): boolean {
    return this.features.metrics;
  }

  checkMetricsEnabled(): boolean {
    return true;
  }

  private async checkForZeropod(): Promise<boolean> {
    // This is a very basic check for Zeropod. It requires the namespace zeropod-system to be present.
    // But it does not check if the Zeropod controller is complete and running.
    let enabled = false;
    try {
      const nsList = await this.kubectl.getNamespaces();
      for (const ns of nsList) {
        if (ns.metadata?.name == 'zeropod-system') {
          enabled = true;
        }
      }
    } catch (_error) {
      this.logger.error('❌ getSleepEnabled: could not check for Zeropod');
      return false;
    }

    return enabled;
  }

  private async runFeatureCheck() {
    this.features.sleep = await this.checkForZeropod();
    this.features.metrics = this.checkMetricsEnabled();
  }

  public getSleepEnabled(): boolean {
    return this.features.sleep;
  }

  public async getRegistry(): Promise<IRegistry> {
    const namespace = process.env.KUSO_NAMESPACE || 'kuso';
    const kusoes = await this.kubectl.getKusoConfig(namespace);
    return kusoes.spec.registry;
  }

  public async getRunpacks(): Promise<Runpack[]> {
    const dbRunpacks = await this.prisma.runpack.findMany({
      include: { fetch: true, build: true, run: true },
    });
    return dbRunpacks.map((rp: any) => new Runpack(rp));
  }

  public async deleteRunpack(id: string): Promise<void> {
    const runpack = await this.prisma.runpack.findUnique({
      where: { id: id },
    });
    if (!runpack) {
      throw new Error(`Runpack with id ${id} not found`);
    }
    await this.prisma.runpack.delete({
      where: { id: id },
    });
    this.logger.log(`Runpack with id ${id} deleted successfully`);
    const m: INotification = {
      name: 'deleteRunpack',
      user: '',
      resource: 'config',
      action: 'delete',
      severity: 'normal',
      message: `Runpack ${runpack.name} deleted`,
      pipelineName: '',
      phaseName: '',
      appName: '',
      data: {
        runpackId: id,
        runpackName: runpack.name,
      },
    };
    await this.notification.send(m);
  }

  public async createRunpack(runpack: any): Promise<DBRunpack> {
    // Create the fetch, build, and run stages first
    const fetch = await this.prisma.runpackPhase.create({
      data: {
        repository: runpack.fetch.repository,
        tag: runpack.fetch.tag,
        command: runpack.fetch.command,
        readOnlyAppStorage: runpack.fetch.readOnlyAppStorage,
        securityContext: {
          create: {
            runAsUser: runpack.fetch.securityContext.runAsUser,
            runAsGroup: runpack.fetch.securityContext.runAsGroup,
            allowPrivilegeEscalation:
              runpack.fetch.securityContext.allowPrivilegeEscalation,
            readOnlyRootFilesystem:
              runpack.fetch.securityContext.readOnlyRootFilesystem,
            runAsNonRoot: runpack.fetch.securityContext.runAsNonRoot,
          },
        },
      },
      include: { securityContext: { include: { capabilities: true } } },
    });

    await this.prisma.capabilityAdd.createMany({
      data: runpack.fetch.securityContext.capabilities.add.map(
        (cap: string) => ({
          capability: cap,
          runpackPhaseId: fetch.id,
        }),
      ),
    });

    await this.prisma.capabilityDrop.createMany({
      data: runpack.fetch.securityContext.capabilities.drop.map(
        (cap: string) => ({
          capability: cap,
          runpackPhaseId: fetch.id,
        }),
      ),
    });

    const build = await this.prisma.runpackPhase.create({
      data: {
        repository: runpack.build.repository,
        tag: runpack.build.tag,
        command: runpack.build.command,
        readOnlyAppStorage: runpack.build.readOnlyAppStorage,
        securityContext: {
          create: {
            runAsUser: runpack.build.securityContext.runAsUser,
            runAsGroup: runpack.build.securityContext.runAsGroup,
            allowPrivilegeEscalation:
              runpack.build.securityContext.allowPrivilegeEscalation,
            readOnlyRootFilesystem:
              runpack.build.securityContext.readOnlyRootFilesystem,
            runAsNonRoot: runpack.build.securityContext.runAsNonRoot,
          },
        },
      },
      include: { securityContext: { include: { capabilities: true } } },
    });

    await this.prisma.capabilityAdd.createMany({
      data: runpack.build.securityContext.capabilities.add.map(
        (cap: string) => ({
          capability: cap,
          runpackPhaseId: build.id,
        }),
      ),
    });

    await this.prisma.capabilityDrop.createMany({
      data: runpack.build.securityContext.capabilities.drop.map(
        (cap: string) => ({
          capability: cap,
          runpackPhaseId: build.id,
        }),
      ),
    });

    const run = await this.prisma.runpackPhase.create({
      data: {
        repository: runpack.run.repository,
        tag: runpack.run.tag,
        command: runpack.run.command,
        readOnlyAppStorage: runpack.run.readOnlyAppStorage,
        securityContext: {
          create: {
            runAsUser: runpack.run.securityContext.runAsUser,
            runAsGroup: runpack.run.securityContext.runAsGroup,
            allowPrivilegeEscalation:
              runpack.run.securityContext.allowPrivilegeEscalation,
            readOnlyRootFilesystem:
              runpack.run.securityContext.readOnlyRootFilesystem,
            runAsNonRoot: runpack.run.securityContext.runAsNonRoot,
          },
        },
      },
      include: { securityContext: { include: { capabilities: true } } },
    });

    await this.prisma.capabilityAdd.createMany({
      data: runpack.run.securityContext.capabilities.add.map((cap: string) => ({
        capability: cap,
        runpackPhaseId: run.id,
      })),
    });

    await this.prisma.capabilityDrop.createMany({
      data: runpack.run.securityContext.capabilities.drop.map(
        (cap: string) => ({
          capability: cap,
          runpackPhaseId: run.id,
        }),
      ),
    });

    // Now create the runpack and connect the phases
    const dbRunpack = await this.prisma.runpack.create({
      data: {
        name: runpack.name,
        language: runpack.language,
        fetch: { connect: { id: fetch.id } },
        build: { connect: { id: build.id } },
        run: { connect: { id: run.id } },
      },
      include: { fetch: true, build: true, run: true },
    });
    return dbRunpack;
  }

  public getClusterIssuer(): { clusterissuer: string } {
    return {
      clusterissuer: process.env.KUSO_CLUSTER_ISSUER || 'letsencrypt-prod',
    };
  }

  public async getPodSizes() {
    // Fetch PodSizes from the database using Prisma
    const dbPodSizes = await this.prisma.podSize.findMany();
    // Map DB results to PodSize class
    return dbPodSizes.map(
      (ps: any) =>
        new PodSize({
          id: ps.id,
          name: ps.name,
          description: ps.description,
          resources: {
            requests: {
              memory: ps.memoryRequest || '',
              cpu: ps.cpuLimit || '',
            },
            limits: {
              memory: ps.memoryLimit || '',
              cpu: ps.cpuLimit || '',
            },
          },
        }),
    );
  }

  public async addPodSize(podSize: PodSize): Promise<PodSize> {
    // Create a new PodSize in the database using Prisma
    const dbPodSize = await this.prisma.podSize.create({
      data: {
        name: podSize.name,
        description: podSize.description,
        memoryLimit: podSize?.resources?.limits?.memory || '',
        cpuLimit: podSize?.resources?.limits?.cpu || '',
        memoryRequest: podSize?.resources?.requests?.memory || '',
        cpuRequest: podSize?.resources?.requests?.cpu || '',
      },
    });
    // Return the created PodSize as an instance of the PodSize class
    return new PodSize({
      name: dbPodSize.name,
      description: dbPodSize.description || '',
      resources: {
        requests: {
          memory: dbPodSize.memoryRequest,
          cpu: dbPodSize.cpuRequest,
        },
        limits: {
          memory: dbPodSize.memoryLimit,
          cpu: dbPodSize.cpuLimit,
        },
      },
    });
  }

  public async deletePodSize(id: string): Promise<void> {
    // Delete a PodSize from the database using Prisma
    const podSize = await this.prisma.podSize.findUnique({
      where: { id: id },
    });
    if (!podSize) {
      throw new Error(`PodSize with id ${id} not found`);
    }
    await this.prisma.podSize.delete({
      where: { id: id },
    });
    this.logger.log(`PodSize with id ${id} deleted successfully`);
  }

  public async updatePodSize(id: string, podSize: PodSize): Promise<PodSize> {
    // Update an existing PodSize in the database using Prisma
    const updatedPodSize = await this.prisma.podSize.update({
      where: { id: id },
      data: {
        name: podSize.name,
        description: podSize.description,
        memoryLimit: podSize?.resources?.limits?.memory || '',
        cpuLimit: podSize?.resources?.limits?.cpu || '',
        memoryRequest: podSize?.resources?.requests?.memory || '',
        cpuRequest: podSize?.resources?.requests?.cpu || '',
      },
    });
    // Return the updated PodSize as an instance of the PodSize class
    return new PodSize({
      name: updatedPodSize.name,
      description: updatedPodSize.description || '',
      resources: {
        requests: {
          memory: updatedPodSize.memoryRequest,
          cpu: updatedPodSize.cpuRequest,
        },
        limits: {
          memory: updatedPodSize.memoryLimit,
          cpu: updatedPodSize.cpuLimit,
        },
      },
    });
  }

  public static getLocalauthEnabled(): boolean {
    // return true only when a non-empty session key exists
    return (
      process.env.KUSO_SESSION_KEY !== undefined &&
      process.env.KUSO_SESSION_KEY !== ''
    );
  }

  public static getGithubEnabled(): boolean {
    // return true only when all required GitHub env vars are present
    return (
      process.env.GITHUB_CLIENT_SECRET !== undefined &&
      process.env.GITHUB_CLIENT_ID !== undefined &&
      process.env.GITHUB_CLIENT_CALLBACKURL !== undefined &&
      process.env.GITHUB_CLIENT_ORG !== undefined
    );
  }

  public static getOauth2Enabled(): boolean {
    // return true only when all required OAuth2 env vars are present
    return (
      process.env.OAUTH2_CLIENT_AUTH_URL !== undefined &&
      process.env.OAUTH2_CLIENT_TOKEN_URL !== undefined &&
      process.env.OAUTH2_CLIENT_ID !== undefined &&
      process.env.OAUTH2_CLIENT_SECRET !== undefined &&
      process.env.OAUTH2_CLIENT_CALLBACKURL !== undefined
    );
  }

  public static getAuthenticationScope(scope: string | undefined): string[] {
    if (!scope) {
      return [];
    }
    return scope.split(' ');
  }

  public getKusoUIVersion(): string {
    if (process.env.npm_package_version == undefined) {
      return '0.0.0';
    }
    return process.env.npm_package_version;
  }
}
