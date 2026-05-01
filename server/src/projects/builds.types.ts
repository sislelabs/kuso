// KusoBuild types. Mirrors the CRD spec in
// operator/config/crd/bases/application.kuso.sislelabs.com_kusobuilds.yaml.

export interface KusoBuildSpec {
  project: string;
  service: string;
  ref: string; // commit SHA (40 hex)
  branch?: string;
  repo: { url: string; path?: string };
  githubInstallationId?: number;
  strategy?: 'dockerfile';
  image: { repository: string; tag: string };
}

export interface KusoBuild {
  apiVersion?: string;
  kind?: string;
  metadata: {
    name: string;
    namespace?: string;
    labels?: Record<string, string>;
    creationTimestamp?: string;
  };
  spec: KusoBuildSpec;
  status?: {
    phase?: 'pending' | 'running' | 'succeeded' | 'failed';
    completedAt?: string;
    message?: string;
  };
}

export interface CreateBuildRequest {
  // Either ref (40-char SHA) OR branch (we resolve to SHA via GitHub).
  ref?: string;
  branch?: string;
}
