import { api } from "@/lib/api-client";
import type { KusoAddon } from "@/types/projects";

export interface CreateAddonBody {
  name: string;
  kind: string;
  // externalCredentials has the server create the source Secret for an
  // external addon from these keys; needs a connection URL key such as
  // DATABASE_URL / REDIS_URL.
  externalCredentials?: Record<string, string>;
  useInstanceAddon?: string;
  version?: string;
  ha?: boolean;
  storageSize?: string;
  tls?: "disable" | "require";
}

export async function createAddon(project: string, body: CreateAddonBody): Promise<KusoAddon> {
  return api(`/api/projects/${encodeURIComponent(project)}/addons`, { method: "POST", body });
}

// Connection-URL key the server's hasConnectionURL accepts, per kind.
export function connectionURLKey(kind: string): string {
  switch (kind) {
    case "redis":
      return "REDIS_URL";
    case "valkey":
      return "VALKEY_URL";
    case "mongodb":
      return "MONGO_URL";
    case "rabbitmq":
      return "AMQP_URL";
    case "nats":
      return "NATS_URL";
    case "clickhouse":
      return "CLICKHOUSE_URL";
    case "redpanda":
      return "KAFKA_BROKERS";
    case "s3":
      return "S3_ENDPOINT";
    default:
      return "DATABASE_URL";
  }
}

export interface SubscribedAddons {
  subscribed: string[];
  available: string[];
}

const subscribedPath = (project: string, service: string) =>
  `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/subscribed-addons`;

export async function getSubscribedAddons(project: string, service: string): Promise<SubscribedAddons> {
  const res = await api<Partial<SubscribedAddons> | null>(subscribedPath(project, service));
  return { subscribed: res?.subscribed ?? [], available: res?.available ?? [] };
}

export async function setSubscribedAddons(
  project: string,
  service: string,
  addons: string[],
): Promise<void> {
  await api(subscribedPath(project, service), { method: "PUT", body: { addons } });
}
