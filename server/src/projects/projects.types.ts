// Type shapes mirroring the v0.2 CRDs (docs/REDESIGN.md).
//
// Kept narrow on purpose — the server's internal IApp / IPipeline shapes
// were huge and dragged most of the codebase along when one field changed.
// New rule: only the fields the API actually returns or accepts.

export interface KusoProjectSpec {
  description?: string;
  baseDomain?: string;
  defaultRepo?: {
    url: string;
    defaultBranch?: string;
  };
  github?: {
    installationId?: number;
  };
  previews?: {
    enabled?: boolean;
    ttlDays?: number;
  };
}

export interface KusoProject {
  apiVersion?: string;
  kind?: string;
  metadata: {
    name: string;
    namespace?: string;
    creationTimestamp?: string;
    uid?: string;
    labels?: Record<string, string>;
  };
  spec: KusoProjectSpec;
  status?: {
    services?: number;
    environments?: number;
    addons?: number;
    ready?: boolean;
    conditions?: any[];
  };
}

export interface KusoServiceSpec {
  project: string;
  repo?: {
    url?: string;
    path?: string;
  };
  runtime?: 'dockerfile' | 'nixpacks' | 'buildpacks' | 'static' | '';
  port?: number;
  domains?: { host: string; tls?: boolean }[];
  envVars?: any[];
  scale?: {
    min?: number;
    max?: number;
    targetCPU?: number;
  };
  sleep?: {
    enabled?: boolean;
    afterMinutes?: number;
  };
}

export interface KusoService {
  apiVersion?: string;
  kind?: string;
  metadata: {
    name: string;
    namespace?: string;
    labels?: Record<string, string>;
  };
  spec: KusoServiceSpec;
  status?: any;
}

export interface KusoEnvironmentSpec {
  project: string;
  service: string;
  kind: 'production' | 'preview';
  branch: string;
  pullRequest?: { number: number; headRef: string };
  ttl?: { expiresAt: string };
  image?: { repository: string; tag: string; pullPolicy?: string };
  port?: number;
  replicaCount?: number;
  autoscaling?: {
    enabled?: boolean;
    minReplicas?: number;
    maxReplicas?: number;
    targetCPUUtilizationPercentage?: number;
  };
  sleep?: { enabled?: boolean };
  host?: string;
  tlsEnabled?: boolean;
  clusterIssuer?: string;
  ingressClassName?: string;
  envVars?: any[];
  envFromSecrets?: string[];
  resources?: any;
}

export interface KusoEnvironment {
  apiVersion?: string;
  kind?: string;
  metadata: {
    name: string;
    namespace?: string;
    labels?: Record<string, string>;
  };
  spec: KusoEnvironmentSpec;
  status?: any;
}

export type KusoAddonKind =
  | 'postgres'
  | 'redis'
  | 'mongodb'
  | 'mysql'
  | 'rabbitmq'
  | 'memcached'
  | 'clickhouse'
  | 'elasticsearch'
  | 'kafka'
  | 'cockroachdb'
  | 'couchdb';

export interface KusoAddonSpec {
  project: string;
  kind: KusoAddonKind;
  version?: string;
  size?: 'small' | 'medium' | 'large';
  ha?: boolean;
  storageSize?: string;
  resources?: any;
  password?: string;
  database?: string;
  backup?: { schedule?: string; retentionDays?: number };
}

export interface KusoAddon {
  apiVersion?: string;
  kind?: string;
  metadata: {
    name: string;
    namespace?: string;
    labels?: Record<string, string>;
  };
  spec: KusoAddonSpec;
  status?: any;
}

// API-shaped DTOs (used by controllers). Distinct from the CR shapes so
// internal renames don't break the wire format.

export interface CreateProjectDTO {
  name: string;
  description?: string;
  baseDomain?: string;
  defaultRepo: {
    url: string;
    defaultBranch?: string;
  };
  github?: {
    installationId?: number;
  };
  previews?: {
    enabled?: boolean;
    ttlDays?: number;
  };
}

export interface CreateServiceDTO {
  name: string;
  repo?: {
    url?: string;
    path?: string;
  };
  runtime?: KusoServiceSpec['runtime'];
  port?: number;
  envVars?: any[];
  scale?: KusoServiceSpec['scale'];
  sleep?: KusoServiceSpec['sleep'];
  domains?: KusoServiceSpec['domains'];
}

export interface CreateAddonDTO {
  name: string;
  kind: KusoAddonKind;
  version?: string;
  size?: 'small' | 'medium' | 'large';
  ha?: boolean;
  storageSize?: string;
  database?: string;
}
