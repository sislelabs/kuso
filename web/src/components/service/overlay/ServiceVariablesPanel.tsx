"use client";

import { useMemo, useState } from "react";
import { EnvVarsEditor } from "@/components/service/EnvVarsEditor";
import { AddOauthAppDialog } from "@/components/service/overlay/AddOauthAppDialog";
import { useEnvironments } from "@/features/projects";
import { envGroupName, isProductionGroup } from "@/lib/env-group";

export function ServiceVariablesPanel({
  project,
  service,
  env,
}: {
  project: string;
  service: string;
  // env-group scope from the overlay header (production / staging /
  // preview-pr-N). The editor shows that env's overrides alongside the
  // service-wide list.
  env: string;
}) {
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

  const [oauthOpen, setOauthOpen] = useState(false);

  return (
    <>
      <EnvVarsEditor
        project={project}
        service={service}
        env={env}
        onAddGithubSignIn={host ? () => setOauthOpen(true) : undefined}
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
