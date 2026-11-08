// The KusoCron spec fields the project views read.
export interface CronNodeSpec {
  project?: string;
  kind?: string;
  service?: string;
  url?: string;
  schedule?: string;
  command?: string[];
  suspend?: boolean;
  displayName?: string;
  image?: { repository?: string; tag?: string; pullPolicy?: string };
  envFromSecrets?: string[];
  concurrencyPolicy?: string;
  activeDeadlineSeconds?: number;
}
