"use client";

import { Handle, Position } from "@xyflow/react";
import Link from "next/link";
import { ExternalLink } from "lucide-react";
import type { KusoEnvironment, KusoService } from "@/types/projects";
import { DeployStatusPill, type DeployStatus } from "@/components/service/DeployStatusPill";
import { SleepBadge } from "@/components/service/SleepBadge";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { cn, serviceShortName } from "@/lib/utils";

export interface ServiceNodeData extends Record<string, unknown> {
  project: string;
  service: KusoService;
  env?: KusoEnvironment;
}

function statusFor(env?: KusoEnvironment): DeployStatus {
  if (!env) return "unknown";
  const phase = (env.status?.phase ?? "").toString().toLowerCase();
  if (phase === "building") return "building";
  if (phase === "deploying") return "deploying";
  if (env.status?.ready) return "active";
  if (phase === "failed" || phase === "error") return "failed";
  if (phase === "sleeping") return "sleeping";
  return "unknown";
}

export function ServiceNode({ data }: { data: ServiceNodeData }) {
  const status = statusFor(data.env);
  const url = data.env?.status?.url as string | undefined;
  const shortName = serviceShortName(data.project, data.service.metadata.name);

  return (
    <div
      className={cn(
        "group w-[260px] rounded-2xl border bg-card p-3 shadow-[var(--shadow-sm)] transition-all",
        "hover:shadow-[var(--shadow-md)]",
        (status === "building" || status === "deploying") &&
          "border-[var(--accent)]/40 animate-pulse",
        status === "active" && "border-emerald-500/30",
        status === "failed" && "border-red-500/30",
        status === "sleeping" && "opacity-60 border-[var(--border-subtle)]",
        !["building", "deploying", "active", "failed", "sleeping"].includes(status) &&
          "border-[var(--border-subtle)]"
      )}
    >
      <Handle type="target" position={Position.Left} className="!bg-[var(--accent)]" />
      <Handle type="source" position={Position.Right} className="!bg-[var(--accent)]" />
      <div className="flex items-center justify-between gap-2">
        <Link
          href={`/projects/${data.project}/services/${shortName}`}
          className="flex items-center gap-2 truncate font-medium text-sm hover:underline"
        >
          <RuntimeIcon runtime={data.service.spec.runtime} />
          <span className="truncate">{shortName}</span>
        </Link>
        <DeployStatusPill status={status} />
      </div>
      <dl className="mt-3 space-y-0.5 font-mono text-[10px]">
        {data.service.spec.runtime && (
          <div className="flex gap-1.5 text-[var(--text-tertiary)]">
            <dt>runtime</dt>
            <dd className="text-[var(--text-secondary)]">{data.service.spec.runtime}</dd>
          </div>
        )}
        {data.service.spec.port !== undefined && (
          <div className="flex gap-1.5 text-[var(--text-tertiary)]">
            <dt>port</dt>
            <dd className="text-[var(--text-secondary)]">{data.service.spec.port}</dd>
          </div>
        )}
        {url && (
          <div className="flex gap-1.5 text-[var(--text-tertiary)]">
            <dt>url</dt>
            <dd className="truncate text-[var(--accent)]">
              <a href={url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 hover:underline">
                {url.replace(/^https?:\/\//, "")}
                <ExternalLink className="h-2.5 w-2.5" />
              </a>
            </dd>
          </div>
        )}
      </dl>
      {status === "sleeping" && <SleepBadge className="mt-2" />}
    </div>
  );
}
