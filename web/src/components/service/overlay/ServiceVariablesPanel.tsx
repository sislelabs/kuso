"use client";

import { useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Github } from "lucide-react";
import { EnvVarsEditor } from "@/components/service/EnvVarsEditor";
import { AddOauthAppDialog } from "@/components/service/overlay/AddOauthAppDialog";
import { useEnvironments } from "@/features/projects";

export function ServiceVariablesPanel({ project, service }: { project: string; service: string }) {
  // Pull the production env's host so the OAuth-app helper knows what
  // to register as the callback URL with GitHub. Same lookup pattern
  // as Settings → Networking (production env carries the rendered
  // hostname; the KusoService spec doesn't).
  const envs = useEnvironments(project);
  const host = useMemo(() => {
    const list = envs.data ?? [];
    const prod = list.find(
      (e) =>
        e.spec.service === service ||
        e.spec.service === `${project}-${service}`,
    );
    return prod?.spec.host ?? "";
  }, [envs.data, project, service]);

  const [oauthOpen, setOauthOpen] = useState(false);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="font-heading text-sm font-semibold tracking-tight text-[var(--text-primary)]">
          Service variables
        </h3>
        <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
          mounted as env on every pod
        </p>
      </div>

      {/* Integration helpers. Adding more (Google, Microsoft, etc.)
          slots in next to the GitHub button — each one fills in the
          OAuth provider's `<PREFIX>_CLIENT_ID/_SECRET` env shape. */}
      {host && (
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setOauthOpen(true)}
          >
            <Github className="size-3.5" />
            Add &ldquo;Sign in with GitHub&rdquo;
          </Button>
        </div>
      )}

      <EnvVarsEditor project={project} service={service} />

      <AddOauthAppDialog
        open={oauthOpen}
        onOpenChange={setOauthOpen}
        project={project}
        service={service}
        serviceHost={host}
      />
    </div>
  );
}
