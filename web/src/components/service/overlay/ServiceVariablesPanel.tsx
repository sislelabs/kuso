"use client";

import { Suspense, useMemo, useState } from "react";
import { useSearchParams } from "next/navigation";
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
  const restartImpact = useMemo(
    () => (envs.data ? restartImpactFromEnvs(envs.data, project, service, envGroupName) : undefined),
    [envs.data, project, service],
  );

  const [oauthOpen, setOauthOpen] = useState(false);

  return (
    <>
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
