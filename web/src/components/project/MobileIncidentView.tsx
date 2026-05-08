"use client";

import type { KusoEnvironment, KusoService } from "@/types/projects";
import { serviceShortName } from "@/lib/utils";
import { useTriggerBuild } from "@/features/services";
import { Button } from "@/components/ui/button";
import { ExternalLink, RotateCcw, ScrollText } from "lucide-react";
import { toast } from "sonner";

interface Props {
  project: string;
  services: KusoService[];
  envs: KusoEnvironment[];
  onSelectService?: (svcName: string, tab?: string) => void;
}

export function MobileIncidentView({ project, services, envs, onSelectService }: Props) {
  return (
    <div className="space-y-3 p-3 sm:hidden">
      <div className="rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-4">
        <h2 className="font-heading text-base font-semibold tracking-tight">Incident mode</h2>
        <p className="mt-1 text-xs text-[var(--text-secondary)]">
          Mobile shows the controls you need during an outage: status, logs, open URL, and redeploy.
        </p>
      </div>
      {services.map((svc) => (
        <MobileServiceCard
          key={svc.metadata.name}
          project={project}
          service={svc}
          env={envs.find((e) => e.spec.service === svc.metadata.name && e.spec.kind === "production")}
          onSelectService={onSelectService}
        />
      ))}
    </div>
  );
}

function MobileServiceCard({
  project,
  service,
  env,
  onSelectService,
}: {
  project: string;
  service: KusoService;
  env?: KusoEnvironment;
  onSelectService?: (svcName: string, tab?: string) => void;
}) {
  const shortName = serviceShortName(project, service.metadata.name);
  const trigger = useTriggerBuild(project, shortName);
  const url = env?.status?.url || service.spec.domains?.[0];
  const phase = env?.status?.phase || "unknown";

  const redeploy = () => {
    trigger.mutate({}, {
      onSuccess: () => toast.success(`Redeploy queued for ${shortName}`),
      onError: (err) => toast.error(err instanceof Error ? err.message : "Redeploy failed"),
    });
  };

  return (
    <article className="rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-4 shadow-[var(--shadow-sm)]">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="truncate font-heading text-sm font-semibold">{shortName}</h3>
          <p className="mt-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            {phase}
          </p>
        </div>
        {url && (
          <a
            href={String(url).startsWith("http") ? String(url) : `https://${url}`}
            target="_blank"
            rel="noreferrer"
            className="rounded-md border border-[var(--border-subtle)] p-2 text-[var(--text-secondary)]"
            aria-label="Open service"
          >
            <ExternalLink className="h-4 w-4" />
          </a>
        )}
      </div>
      <div className="mt-4 grid grid-cols-2 gap-2">
        <Button type="button" variant="outline" size="sm" onClick={() => onSelectService?.(shortName, "logs")}>
          <ScrollText className="mr-1.5 h-3.5 w-3.5" /> Logs
        </Button>
        <Button type="button" size="sm" onClick={redeploy} disabled={trigger.isPending}>
          <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
          {trigger.isPending ? "Queuing" : "Redeploy"}
        </Button>
      </div>
    </article>
  );
}
