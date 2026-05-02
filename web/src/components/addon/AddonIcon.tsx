import { Database, HardDrive, Zap, MessageSquare } from "lucide-react";
import { cn } from "@/lib/utils";

const map: Record<string, { color: string; label: string }> = {
  postgres: { color: "text-blue-500", label: "Postgres" },
  postgresql: { color: "text-blue-500", label: "Postgres" },
  redis: { color: "text-red-500", label: "Redis" },
  mongodb: { color: "text-green-500", label: "MongoDB" },
  mysql: { color: "text-orange-500", label: "MySQL" },
  rabbitmq: { color: "text-orange-500", label: "RabbitMQ" },
  memcached: { color: "text-blue-600", label: "Memcached" },
  clickhouse: { color: "text-yellow-500", label: "ClickHouse" },
  elasticsearch: { color: "text-yellow-600", label: "Elasticsearch" },
  kafka: { color: "text-purple-500", label: "Kafka" },
  cockroachdb: { color: "text-blue-700", label: "CockroachDB" },
  couchdb: { color: "text-red-600", label: "CouchDB" },
};

export function AddonIcon({
  kind,
  className,
}: {
  kind?: string;
  className?: string;
}) {
  const m = map[kind ?? ""] ?? { color: "text-[var(--text-tertiary)]", label: kind ?? "?" };
  const Icon =
    kind === "redis" || kind === "memcached"
      ? Zap
      : kind === "rabbitmq" || kind === "kafka"
        ? MessageSquare
        : kind === "elasticsearch" || kind === "clickhouse"
          ? HardDrive
          : Database;
  return <Icon className={cn("h-4 w-4", m.color, className)} />;
}

export function addonLabel(kind?: string): string {
  return map[kind ?? ""]?.label ?? kind ?? "?";
}
