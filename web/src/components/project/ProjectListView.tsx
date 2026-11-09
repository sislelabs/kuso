"use client";

import { useLiveServiceUrl } from "@/features/projects/liveDomain";
import { cronsForEnv, lastDeployedAt } from "@/features/projects/facts";
import { useState } from "react";
import { Check, Clock, Copy, ExternalLink } from "lucide-react";
import { toast } from "sonner";
import type { KusoAddon, KusoEnvironment, KusoService } from "@/types/projects";
import {
  deriveAddonStatus,
  deriveCronStatus,
  deriveServiceStatus,
  serviceUrl,
  STATUS_TONE,
  useLatestBuilds,
  useProjectCrons,
  type NodeStatus,
} from "@/features/services";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { describeSchedule, scheduleTitle } from "@/components/canvas/CronNode";
import { EditCronDialog } from "@/components/cron/EditCronDialog";
import { isProductionGroup } from "@/lib/env-group";
import { relativeTime } from "@/lib/format";
import { cn, serviceShortName } from "@/lib/utils";
import { sortBySeverity } from "./listSort";

interface Props {
  project: string;
  selectedEnv: string;
  services: KusoService[];
  addons: KusoAddon[];
  envs: KusoEnvironment[];
  onSelectService: (short: string, tab?: string) => void;
  onSelectAddon: (name: string, tab?: string) => void;
}

const RUNTIME_LABEL: Record<string, string> = {
  dockerfile: "Dockerfile",
  nixpacks: "Nixpacks",
  buildpacks: "Buildpacks",
  static: "Static",
  image: "Image",
  worker: "Worker",
};

// ProjectListView is the table alternative to the canvas: every service,
// addon and cron in the selected env group with the same derived status
// the canvas nodes show. Rows open the same overlays.
export function ProjectListView({ project, selectedEnv, services, addons, envs, onSelectService, onSelectAddon }: Props) {
  const latestBuilds = useLatestBuilds(project, selectedEnv);
  const crons = useProjectCrons(project);
  const cronRows = cronsForEnv(crons.data ?? [], services, selectedEnv, project);
  const [editCron, setEditCron] = useState<string | null>(null);
  const serviceRows = sortBySeverity(
    services.map((s) => {
      const short = serviceShortName(project, s.metadata.name);
      const forSvc = envs.filter((e) => e.spec.service === s.metadata.name);
      const env = forSvc.find((e) => isProductionGroup(e)) ?? forSvc[0];
      const status = deriveServiceStatus({ stopped: !!s.spec.stopped, env, latestBuild: latestBuilds.data?.[short] });
      return { s, short, env, status, name: s.spec.displayName?.trim() || short };
    }),
  );
  const addonRows = sortBySeverity(
    addons.map((a) => ({ a, status: deriveAddonStatus(a), name: serviceShortName(project, a.metadata.name) })),
  );

  return (
    <div className="min-h-0 flex-1 overflow-auto px-6 py-5">
      <table className="w-full min-w-[560px] border-separate border-spacing-0 text-sm">
        <caption className="sr-only">Services, addons and crons in {project} ({selectedEnv})</caption>
        <thead>
          <tr className="text-left text-xs text-[var(--text-tertiary)]">
            <th scope="col" className="pb-2 pl-3 font-medium">Name</th>
            <th scope="col" className="pb-2 font-medium">Status</th>
            <th scope="col" className="pb-2 font-medium">URL</th>
            <th scope="col" className="pb-2 font-medium">Last deploy</th>
            <th scope="col" className="hidden pb-2 font-medium lg:table-cell">Replicas</th>
            <th scope="col" className="pb-2 pr-3 font-medium">Type</th>
          </tr>
        </thead>

        {services.length > 0 && (
          <tbody>
            <GroupRow label="Services" count={services.length} />
            {serviceRows.map(({ s, short, env, status, name }) => {
              const url = serviceUrl(s, env);
              const deployed = lastDeployedAt(env);
              const r = env?.status?.replicas as { ready?: number; desired?: number; max?: number } | undefined;
              const runtime = s.spec.runtime || "dockerfile";
              return (
                <Row key={s.metadata.name} onOpen={() => onSelectService(short)}>
                  <NameCell
                    icon={<RuntimeIcon runtime={runtime} />}
                    name={name}
                    onOpen={() => onSelectService(short)}
                    focusKey={`service:${short}`}
                    hint={name !== short ? short : undefined}
                  />
                  <td className={cellCls}>
                    <StatusCell status={status} />
                  </td>
                  <td className={cellCls}>
                    {url ? (
                      <LiveUrlCell project={project} service={short} env={env} url={url} />
                    ) : (
                      <span className="text-[var(--text-tertiary)]">{s.spec.internal ? "Internal only" : "—"}</span>
                    )}
                  </td>
                  <td className={cn(cellCls, "whitespace-nowrap text-[var(--text-secondary)]")}>
                    {deployed ? (
                      <time dateTime={deployed} title={new Date(deployed).toLocaleString()}>
                        {relativeTime(deployed)}
                      </time>
                    ) : (
                      <span className="text-[var(--text-tertiary)]">—</span>
                    )}
                  </td>
                  <td className={cn(cellCls, "hidden whitespace-nowrap tabular-nums text-[var(--text-secondary)] lg:table-cell")}>
                    {r && (r.ready !== undefined || r.desired !== undefined) ? (
                      <span title={r.max && r.max > (r.desired ?? 0) ? `Autoscales up to ${r.max}` : undefined}>
                        {r.ready ?? 0}/{r.desired ?? r.max ?? 0}
                        {r.max && r.max > (r.desired ?? 0) ? (
                          <span className="text-[var(--text-tertiary)]"> · max {r.max}</span>
                        ) : null}
                      </span>
                    ) : (
                      <span className="text-[var(--text-tertiary)]">—</span>
                    )}
                  </td>
                  <td className={cn(cellCls, "pr-3 text-[var(--text-secondary)]")}>{RUNTIME_LABEL[runtime] ?? runtime}</td>
                </Row>
              );
            })}
          </tbody>
        )}

        {addons.length > 0 && (
          <tbody>
            <GroupRow label="Addons" count={addons.length} />
            {addonRows.map(({ a, status, name: short }) => {
              const kind = addonLabel(a.spec.kind) + (a.spec.version ? ` ${a.spec.version}` : "");
              return (
                <Row key={a.metadata.name} onOpen={() => onSelectAddon(a.metadata.name)}>
                  <NameCell
                    icon={<AddonIcon kind={a.spec.kind} className="h-4 w-4" />}
                    name={short}
                    onOpen={() => onSelectAddon(a.metadata.name)}
                    focusKey={`addon:${a.metadata.name}`}
                  />
                  <td className={cellCls}>
                    <StatusCell status={status} />
                  </td>
                  <td className={cn(cellCls, "text-[var(--text-tertiary)]")}>—</td>
                  <td className={cn(cellCls, "text-[var(--text-tertiary)]")}>—</td>
                  <td className={cn(cellCls, "hidden whitespace-nowrap text-[var(--text-secondary)] lg:table-cell")}>
                    {a.spec.ha ? "HA" : "Single"}
                  </td>
                  <td className={cn(cellCls, "pr-3 text-[var(--text-secondary)]")}>
                    {kind}
                    {a.spec.storageSize ? <span className="text-[var(--text-tertiary)]"> · {a.spec.storageSize}</span> : null}
                  </td>
                </Row>
              );
            })}
          </tbody>
        )}

        {cronRows.length > 0 && (
          <tbody>
            <GroupRow label="Crons" count={cronRows.length} />
            {cronRows.map((c) => {
              const name = c.spec.displayName?.trim() || serviceShortName(project, c.metadata.name);
              const status = deriveCronStatus(c.spec);
              const kind = (c.spec.kind || "service").toLowerCase();
              return (
                <Row key={c.metadata.name} onOpen={() => setEditCron(c.metadata.name)}>
                  <NameCell
                    icon={<Clock className="h-3.5 w-3.5 text-[var(--text-secondary)]" />}
                    name={name}
                    onOpen={() => setEditCron(c.metadata.name)}
                  />
                  <td className={cellCls}>
                    <StatusCell status={status} />
                  </td>
                  <td className={cn(cellCls, "text-[var(--text-tertiary)]")}>—</td>
                  <td className={cn(cellCls, "text-[var(--text-tertiary)]")}>—</td>
                  <td className={cn(cellCls, "hidden text-[var(--text-tertiary)] lg:table-cell")}>—</td>
                  <td className={cn(cellCls, "pr-3 text-[var(--text-secondary)]")}>
                    {kind === "http" ? "HTTP" : kind === "command" ? "Command" : "Service"} ·{" "}
                    <span title={scheduleTitle(c.spec.schedule ?? "")}>{describeSchedule(c.spec.schedule ?? "")}</span>
                  </td>
                </Row>
              );
            })}
          </tbody>
        )}
      </table>
      <EditCronDialog
        project={project}
        cron={editCron ? (cronRows.find((c) => c.metadata.name === editCron) ?? null) : null}
        onClose={() => setEditCron(null)}
      />
      {crons.isError && (
        <p className="mt-3 text-xs text-[var(--text-tertiary)]">Couldn&apos;t load crons for this project.</p>
      )}
    </div>
  );
}

const cellCls = "border-b border-[var(--border-subtle)] py-2.5 pr-4 align-middle";

function GroupRow({ label, count }: { label: string; count: number }) {
  return (
    <tr>
      <th
        scope="rowgroup"
        colSpan={6}
        className="pb-1.5 pl-3 pt-5 text-left text-xs font-medium text-[var(--text-secondary)] first:pt-2"
      >
        {label} <span className="font-normal text-[var(--text-tertiary)]">{count}</span>
      </th>
    </tr>
  );
}

// Rows are clickable for mouse users; keyboard users get the name button.
function Row({ onOpen, children }: { onOpen: () => void; children: React.ReactNode }) {
  return (
    <tr
      onClick={(e) => {
        if ((e.target as HTMLElement).closest("a,button")) return;
        onOpen();
      }}
      className="group cursor-pointer transition-colors hover:bg-[var(--bg-tertiary)]/50"
    >
      {children}
    </tr>
  );
}

function NameCell({
  icon,
  name,
  hint,
  onOpen,
  focusKey,
}: {
  icon: React.ReactNode;
  name: string;
  hint?: string;
  onOpen?: () => void;
  // Lets a closing overlay hand focus back to this row.
  focusKey?: string;
}) {
  return (
    <th scope="row" className={cn(cellCls, "pl-3 text-left font-normal")}>
      <span className="flex min-w-0 items-center gap-2.5">
        <span className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
          {icon}
        </span>
        {onOpen ? (
          <button
            type="button"
            onClick={onOpen}
            data-focus-key={focusKey}
            className="min-w-0 truncate rounded text-left font-medium text-[var(--text-primary)] hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--accent)]"
          >
            {name}
          </button>
        ) : (
          <span className="min-w-0 truncate font-medium text-[var(--text-primary)]">{name}</span>
        )}
        {hint && <span className="truncate font-mono text-xs text-[var(--text-tertiary)]">{hint}</span>}
      </span>
    </th>
  );
}

export function StatusCell({ status }: { status: NodeStatus }) {
  const tone = STATUS_TONE[status.tone];
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap" title={status.detail}>
      <span aria-hidden className={cn("h-2 w-2 shrink-0 rounded-full", tone.dot)} />
      <span className={cn("text-sm", status.tone === "muted" ? "text-[var(--text-secondary)]" : tone.text)}>
        {status.label}
      </span>
    </span>
  );
}

function LiveUrlCell({ project, service, env, url }: { project: string; service: string; env?: KusoEnvironment; url: string }) {
  return <UrlCell url={useLiveServiceUrl(project, service, env, url) ?? url} />;
}

function UrlCell({ url }: { url: string }) {
  const [copied, setCopied] = useState(false);
  const display = url.replace(/^https?:\/\//, "");
  return (
    <span className="inline-flex max-w-[200px] items-center gap-1 lg:max-w-[280px]">
      <a
        href={url}
        target="_blank"
        rel="noreferrer"
        className="inline-flex min-w-0 items-center gap-1 font-mono text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:underline"
      >
        <span className="truncate">{display}</span>
        <ExternalLink className="h-3 w-3 shrink-0" aria-hidden />
        <span className="sr-only">(opens in a new tab)</span>
      </a>
      <button
        type="button"
        aria-label={`Copy ${display}`}
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(url);
            setCopied(true);
            toast.success("URL copied");
            window.setTimeout(() => setCopied(false), 1200);
          } catch {
            toast.error("Couldn't copy");
          }
        }}
        className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-[var(--text-tertiary)] opacity-0 transition-opacity hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:opacity-100 group-hover:opacity-100"
      >
        {copied ? <Check className="h-3.5 w-3.5 text-[var(--success)]" /> : <Copy className="h-3.5 w-3.5" />}
      </button>
    </span>
  );
}
