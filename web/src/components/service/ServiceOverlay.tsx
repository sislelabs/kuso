"use client";

import { Sheet, SheetContent } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { Skeleton } from "@/components/ui/skeleton";
import { useService } from "@/features/services";
import { useEnvironments } from "@/features/projects";
import { ServiceDeploymentsPanel } from "./overlay/ServiceDeploymentsPanel";
import { ServiceVariablesPanel } from "./overlay/ServiceVariablesPanel";
import { ServiceMetricsPanel } from "./overlay/ServiceMetricsPanel";
import { ServiceSettingsPanel } from "./overlay/ServiceSettingsPanel";

interface Props {
  project: string;
  service: string | null;
  // env is the URL form selected in the TopNav: "production" or a
  // short preview name (e.g. "pr-42"). The overlay uses it to pick
  // the right environment for the URL/status header line.
  env?: string;
  onOpenChange: (open: boolean) => void;
  initialTab?: OverlayTab;
}

export type OverlayTab = "deployments" | "variables" | "metrics" | "settings";

// ServiceOverlay slides in from the right when a service node is clicked
// on the project canvas (or its row in the list view). It replaces the
// old standalone /projects/<p>/services/<s> route — the flow lives at
// the same URL with a ?service=<short> query param so refresh re-opens.
//
// Width is wider than the default Sheet (3/4 → max sm) because the
// deployments + settings sections are dense.
export function ServiceOverlay({ project, service, env: envParam = "production", onOpenChange, initialTab = "deployments" }: Props) {
  const open = service !== null && service !== "";
  const svc = useService(project, service ?? "");
  const envs = useEnvironments(project);

  const fqn = service ? project + "-" + service : "";
  const env = (envs.data ?? []).find((e) => {
    if (e.spec.service !== fqn) return false;
    if (envParam === "production") return e.spec.kind === "production";
    const short = e.metadata.name.split("-").slice(-2).join("-");
    return short === envParam;
  });

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        // Override the default 3/4-width / sm:max-w-sm sizing — the
        // overlay needs room for env-var rows and deployment lines.
        className="w-full max-w-3xl sm:max-w-3xl flex flex-col gap-0 p-0"
      >
        <header className="flex items-start gap-3 border-b border-[var(--border-subtle)] px-6 py-5">
          <span className="mt-0.5 inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--accent)]">
            <RuntimeIcon runtime={svc.data?.spec.runtime} />
          </span>
          <div className="min-w-0 flex-1">
            <h2 className="font-heading text-xl font-semibold tracking-tight truncate">
              {service ?? ""}
            </h2>
            <p className="mt-0.5 truncate font-mono text-[10px] text-[var(--text-tertiary)]">
              {project}
              {svc.data?.spec.repo?.url && (
                <> · {svc.data.spec.repo.url.replace(/^https?:\/\/(www\.)?/, "")}</>
              )}
            </p>
          </div>
        </header>

        {svc.isPending ? (
          <div className="p-6 space-y-3">
            <Skeleton className="h-8 w-48" />
            <Skeleton className="h-32 w-full" />
            <Skeleton className="h-32 w-full" />
          </div>
        ) : svc.isError ? (
          <div className="p-6 text-sm text-red-500">
            Failed to load service: {svc.error?.message}
          </div>
        ) : (
          <Tabs defaultValue={initialTab} className="flex-1 flex flex-col min-h-0">
            <TabsList
              className="!h-auto !rounded-none !border-b border-[var(--border-subtle)] !bg-transparent !p-0 px-6 gap-6"
            >
              <OverlayTabTrigger value="deployments">Deployments</OverlayTabTrigger>
              <OverlayTabTrigger value="variables">Variables</OverlayTabTrigger>
              <OverlayTabTrigger value="metrics">Metrics</OverlayTabTrigger>
              <OverlayTabTrigger value="settings">Settings</OverlayTabTrigger>
            </TabsList>

            <TabsContent value="deployments" className="flex-1 min-h-0 overflow-y-auto p-6">
              <ServiceDeploymentsPanel project={project} service={service ?? ""} env={env} />
            </TabsContent>
            <TabsContent value="variables" className="flex-1 min-h-0 overflow-y-auto p-6">
              <ServiceVariablesPanel project={project} service={service ?? ""} />
            </TabsContent>
            <TabsContent value="metrics" className="flex-1 min-h-0 overflow-y-auto p-6">
              <ServiceMetricsPanel project={project} service={service ?? ""} />
            </TabsContent>
            <TabsContent value="settings" className="flex-1 min-h-0 overflow-y-auto p-0">
              <ServiceSettingsPanel project={project} service={service ?? ""} svc={svc.data} />
            </TabsContent>
          </Tabs>
        )}
      </SheetContent>
    </Sheet>
  );
}

function OverlayTabTrigger({ value, children }: { value: string; children: React.ReactNode }) {
  return (
    <TabsTrigger
      value={value}
      className="!rounded-none !bg-transparent !shadow-none !border-0 !border-b-2 !border-transparent data-[selected]:!border-[var(--text-primary)] data-[selected]:!bg-transparent !px-0 !py-3 text-sm font-medium text-[var(--text-tertiary)] data-[selected]:text-[var(--text-primary)] hover:text-[var(--text-secondary)] transition-colors"
    >
      {children}
    </TabsTrigger>
  );
}
