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
  // alwaysOn=true overrides every per-service sleep config so all
  // services in this project run with scale-to-zero disabled.
  alwaysOn?: boolean;
}

export interface KusoProject {
  metadata: KusoMeta;
  spec: KusoProjectSpec;
  status?: Record<string, unknown>;
}

export interface KusoServiceSpec {
  project: string;
  // Free-form display label shown in the canvas + overlay header.
  // Empty string falls back to the URL slug (the short name). Edit
  // via PATCH /api/projects/.../services/.../{displayName}; renaming
  // the slug is a separate destructive flow.
  displayName?: string;
  repo?: KusoRepoRef;
  runtime?: "dockerfile" | "nixpacks" | "buildpacks" | "static";
  port?: number;
  domains?: { host?: string; tls?: boolean }[];
  envVars?: KusoEnvVar[];
  scale?: { min?: number; max?: number; targetCPU?: number };
  // Pod CPU/memory requests+limits (k8s ResourceRequirements shape).
  resources?: {
    requests?: { cpu?: string; memory?: string };
    limits?: { cpu?: string; memory?: string };
  };
  sleep?: { enabled?: boolean; afterMinutes?: number };
  static?: {
    builderImage?: string;
    runtimeImage?: string;
    buildCmd?: string;
    outputDir?: string;
  };
  buildpacks?: { builderImage?: string; lifecycleImage?: string };
  volumes?: KusoVolume[];
  placement?: { labels?: Record<string, string>; nodes?: string[] };
}

export interface KusoVolume {
  name: string;
  mountPath: string;
  sizeGi?: number;
  storageClass?: string;
  accessMode?: string;
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
  // production: the always-on env auto-created with each KusoService
  // preview:    PR-driven ephemeral (managed by kuso-server's GH
  //             dispatcher)
  // custom:     user-created long-lived (staging, qa, demo). Added in
  //             v0.16.4 so the env-switcher could distinguish these
  //             from production in the UI without breaking helm chart
  //             value defaults.
  kind: "production" | "preview" | "custom";
  branch?: string;
  pullRequest?: { number?: number; headRef?: string };
  ttl?: { expiresAt?: string };
  // host/tlsEnabled drive the public URL — the canvas reads them to
  // detect ${{ svc.PUBLIC_URL }} edges where the resolved value
  // contains the env's host. Optional because preview envs may lack
  // an ingress.
  host?: string;
  // additionalHosts: per-env custom domains. v0.16.19+ this is the
  // sole source of truth for non-primary hostnames; service-level
  // spec.domains no longer propagates here. The chart renders one
  // Ingress rule per (host, ...additionalHosts).
  additionalHosts?: string[];
  tlsEnabled?: boolean;
  // envFromSecrets is the list of Kubernetes Secret names that get
  // envFrom-mounted into every pod for this env. The server stamps
  // every project addon's "<addon>-conn" secret into here at create
  // time, so the canvas can detect "service has access to addon X"
  // even when the user never wrote an explicit ${{ x.URL }} ref.
  envFromSecrets?: string[];
  // envVars: the propagated env-var list (svc.spec.envVars merged
  // with subscribed shared-secret valueFrom entries + per-env
  // overrides). The canvas reads this to detect service-to-service
  // edges that come from URL-shaped key names mounted via
  // valueFrom.secretKeyRef (no literal value visible client-side).
  envVars?: KusoEnvVar[];
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
  backup?: {
    // 5-field cron expression (e.g. "0 3 * * *"). Empty string =
    // backups disabled (chart drops the CronJob entirely).
    schedule?: string;
    // 0 = keep forever; >0 = prune objects older than N days each
    // backup run.
    retentionDays?: number;
  };
  // pooler: opt-in PgBouncer in front of a kind=postgres addon.
  // When enabled the addon's <name>-conn Secret gains
  // POOLER_HOST/POOLER_PORT/POOLER_URL keys; DATABASE_URL stays
  // direct. Ignored for non-postgres kinds.
  pooler?: {
    enabled?: boolean;
  };
  // publicTCP: opt-in public TCP endpoint for the addon. enabled is
  // the user toggle; port is server-allocated from the cluster's
  // configured pool (KUSO_TCP_PROXY_PORTS) and stamped here once the
  // POST /public-tcp endpoint runs. Admin-only.
  publicTCP?: {
    enabled?: boolean;
    port?: number;
  };
  // webUI: opt-in reverse-proxy access to the addon's built-in web
  // console (mailpit's mail viewer, NATS monitor, ...). When enabled
  // the kuso server proxies the addon's HTTP UI at
  // /api/projects/<p>/addons/<a>/webui/ — no new ingress, no
  // per-UI password (session-gated). Kinds without a known UI port
  // silently no-op.
  webUI?: {
    enabled?: boolean;
  };
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
