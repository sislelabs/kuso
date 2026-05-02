// Project / service / env / addon wire shapes. Each one mirrors the
// kube.KusoX struct from server-go/internal/kube/types.go.

export interface KusoMeta {
  name: string;
  namespace?: string;
  uid?: string;
  creationTimestamp?: string;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
}

export interface KusoRepoRef {
  url?: string;
  defaultBranch?: string;
  path?: string;
}

export interface KusoProjectSpec {
  description?: string;
  baseDomain?: string;
  namespace?: string;
  defaultRepo?: KusoRepoRef;
  github?: { installationId?: number };
  previews?: { enabled?: boolean; ttlDays?: number };
}

export interface KusoProject {
  metadata: KusoMeta;
  spec: KusoProjectSpec;
  status?: Record<string, unknown>;
}

export interface KusoServiceSpec {
  project: string;
  repo?: KusoRepoRef;
  runtime?: "dockerfile" | "nixpacks" | "buildpacks" | "static";
  port?: number;
  domains?: { host?: string; tls?: boolean }[];
  envVars?: KusoEnvVar[];
  scale?: { min?: number; max?: number; targetCPU?: number };
  sleep?: { enabled?: boolean; afterMinutes?: number };
  static?: {
    builderImage?: string;
    runtimeImage?: string;
    buildCmd?: string;
    outputDir?: string;
  };
  buildpacks?: { builderImage?: string; lifecycleImage?: string };
}

export interface KusoEnvVar {
  name?: string;
  value?: string;
  valueFrom?: Record<string, unknown>;
}

export interface KusoService {
  metadata: KusoMeta;
  spec: KusoServiceSpec;
  status?: Record<string, unknown>;
}

export interface KusoEnvironmentSpec {
  project: string;
  service: string;
  kind: "production" | "preview";
  branch?: string;
  pullRequest?: { number?: number; headRef?: string };
  ttl?: { expiresAt?: string };
}

export interface KusoEnvironment {
  metadata: KusoMeta;
  spec: KusoEnvironmentSpec;
  status?: {
    commit?: string;
    imageTag?: string;
    ready?: boolean;
    url?: string;
    lastDeployedAt?: string;
    phase?: string;
    [k: string]: unknown;
  };
}

export interface KusoAddonSpec {
  project: string;
  kind: string;
  version?: string;
  size?: "small" | "medium" | "large";
  ha?: boolean;
  storageSize?: string;
  database?: string;
}

export interface KusoAddon {
  metadata: KusoMeta;
  spec: KusoAddonSpec;
  status?: {
    ready?: boolean;
    connectionSecret?: string;
    endpoint?: string;
    [k: string]: unknown;
  };
}

// Health rollup computed client-side from a project's services + envs.
export type ProjectHealth = "healthy" | "building" | "failed" | "sleeping" | "empty";
