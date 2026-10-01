"use client";

import { useMemo, useState } from "react";
import { EnvVarsEditor } from "@/components/service/EnvVarsEditor";
import { AddOauthAppDialog } from "@/components/service/overlay/AddOauthAppDialog";
import { useEnvironments } from "@/features/projects";
import { isProductionGroup } from "@/lib/env-group";

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
  // Pull the production env's host so the OAuth-app helper knows what
  // to register as the callback URL with GitHub. Same lookup pattern
  // as Settings → Networking (production env carries the rendered
  // hostname; the KusoService spec doesn't).
  const envs = useEnvironments(project);
  const host = useMemo(() => {
    const list = envs.data ?? [];
    const prod = list.find(
      (e) =>
        (e.spec.service === service || e.spec.service === `${project}-${service}`) &&
        isProductionGroup(e),
    );
    return prod?.spec.host ?? "";
  }, [envs.data, project, service]);

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
