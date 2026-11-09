"use client";

import { Suspense, useMemo, useState } from "react";
import { useSearchParams } from "next/navigation";
import { CopyButton } from "@/components/ui/copy-button";
import { EnvVarsEditor } from "@/components/service/EnvVarsEditor";
import { AddOauthAppDialog } from "@/components/service/overlay/AddOauthAppDialog";
import { restartImpactFromEnvs } from "@/components/service/envvars/restartImpact";
import { useEnvironments } from "@/features/projects";
import { envGroupName, isProductionGroup } from "@/lib/env-group";

type PanelProps = {
  project: string;
  service: string;
  // env-group scope from the overlay header (production / staging /
  // preview-pr-N). The editor shows that env's overrides alongside the
  // service-wide list.
  env: string;
};

// ?highlight=<KEY> (command palette / notification deep links) scrolls to
// and flashes that variable. useSearchParams needs a Suspense boundary in
// the static export, so the param is read in a child that falls back to
// rendering the editor without it.
export function ServiceVariablesPanel(props: PanelProps) {
  return (
    <Suspense fallback={<VariablesPanelBody {...props} highlight={null} />}>
      <WithHighlight {...props} />
    </Suspense>
  );
}

function WithHighlight(props: PanelProps) {
  const search = useSearchParams();
  const tab = search?.get("tab");
  // Only honour it for a link aimed at this tab; other tabs reuse
  // ?highlight for their own anchors (e.g. a log line).
  const highlight = !tab || tab === "variables" ? search?.get("highlight") ?? null : null;
  return <VariablesPanelBody {...props} highlight={highlight} />;
}

function VariablesPanelBody({ project, service, env, highlight }: PanelProps & { highlight: string | null }) {
  // Host of the selected env-group's env, so the OAuth-app helper
  // registers the callback URL of the env being edited (a staging
  // clone needs the staging host, not production's). Falls back to the
  // production env when the selected group has no env/host for this
  // service. The KusoService spec doesn't carry the rendered hostname.
  const envs = useEnvironments(project);
  const host = useMemo(() => {
    const mine = (envs.data ?? []).filter(
      (e) => e.spec.service === service || e.spec.service === `${project}-${service}`,
    );
    const selected = mine.find((e) => envGroupName(e) === env);
    const prod = mine.find((e) => isProductionGroup(e));
    return selected?.spec.host || prod?.spec.host || "";
  }, [envs.data, project, service, env]);
  // Every env-group this service runs in reads the same variable list;
  // only the "<env> only" rows differ.
  const groups = useMemo(() => {
    const names = new Set<string>();
    for (const e of envs.data ?? []) {
      if (e.spec.service === service || e.spec.service === `${project}-${service}`) names.add(envGroupName(e));
    }
    return Array.from(names).sort((a, b) => (a === "production" ? -1 : b === "production" ? 1 : a.localeCompare(b)));
  }, [envs.data, project, service]);
  const restartImpact = useMemo(
    () => (envs.data ? restartImpactFromEnvs(envs.data, project, service, envGroupName) : undefined),
    [envs.data, project, service],
  );

  const [oauthOpen, setOauthOpen] = useState(false);

  return (
    <>
      {groups.length > 1 && <SharedScopeBanner groups={groups} env={env} project={project} service={service} />}
      <EnvVarsEditor
        project={project}
        service={service}
        env={env}
        onAddGithubSignIn={host ? () => setOauthOpen(true) : undefined}
        restartImpact={restartImpact}
        highlight={highlight}
      />
      <AddOauthAppDialog
        open={oauthOpen}
        onOpenChange={setOauthOpen}
        project={project}
        service={service}
        serviceHost={host}
      />
    </>
  );
}

function listJoin(items: string[]): string {
  if (items.length <= 2) return items.join(" and ");
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

// SharedScopeBanner says out loud that a save here lands in every
// environment, and how to pin a value to just one (the editor marks
// existing pins "<env> only"; writing one is a CLI action today).
function SharedScopeBanner({
  groups,
  env,
  project,
  service,
}: {
  groups: string[];
  env: string;
  project: string;
  service: string;
}) {
  const shown = groups.length > 4 ? [...groups.slice(0, 3), `${groups.length - 3} more`] : groups;
  const cmd = `kuso env set ${project} ${service} KEY=value --env ${env}`;
  return (
    <div className="mb-4 rounded-md border border-[var(--info)]/30 bg-[var(--info-subtle)] px-3 py-2.5 text-[13px] text-[var(--text-primary)]">
      <p>
        <span className="font-medium">Shared by {listJoin(shown)}.</span>{" "}
        <span className="text-[var(--text-secondary)]">Changes here are staged and apply to every environment when you deploy them.</span>
      </p>
      <p className="mt-1 text-xs text-[var(--text-secondary)]">
        {env === "production" ? (
          <>Values pinned to one environment show an &ldquo;only&rdquo; badge when you switch to that environment.</>
        ) : (
          <>
            Values pinned to {env} show a &ldquo;{env} only&rdquo; badge. To pin one:{" "}
            <code className="rounded bg-[var(--bg-tertiary)] px-1 font-mono text-[var(--text-primary)]">{cmd}</code>
            <CopyButton value={cmd} label="Copy command" size="xs" />
          </>
        )}
      </p>
    </div>
  );
}
