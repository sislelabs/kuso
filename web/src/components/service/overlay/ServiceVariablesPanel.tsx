"use client";

import { useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Github } from "lucide-react";
import { EnvVarsEditor } from "@/components/service/EnvVarsEditor";
import { AddOauthAppDialog } from "@/components/service/overlay/AddOauthAppDialog";
import { useEnvironments } from "@/features/projects";
import { useServiceEnvOverrides } from "@/features/services";

export function ServiceVariablesPanel({
  project,
  service,
  env,
}: {
  project: string;
  service: string;
  // env-group scope from the overlay header (production / staging /
  // preview-pr-N). Drives the per-env secret-key fetch in
  // EnvVarsEditor — without it, the editor only sees shared-secret
  // subscriptions + spec.envVars, so per-env NEXT_PUBLIC_* keys mounted
  // via envFromSecrets get flagged as "referenced but not set" even
  // when they're actually live on the pod.
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
        e.spec.service === service ||
        e.spec.service === `${project}-${service}`,
    );
    return prod?.spec.host ?? "";
  }, [envs.data, project, service]);

  const [oauthOpen, setOauthOpen] = useState(false);
  const overrides = useServiceEnvOverrides(project, service, env);
  const overrideVars = overrides.data?.envVars ?? [];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="font-heading text-sm font-semibold tracking-tight text-[var(--text-primary)]">
          Service variables
        </h3>
        <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
          shared by every environment
        </p>
      </div>

      {/* The editor below always reads and writes the service-wide list;
          per-env overrides are only writable via `kuso env set --env`. */}
      {env !== "production" && (
        <p className="rounded-md border border-[var(--warning)]/30 bg-[var(--warning-subtle)] p-3 text-[11px] leading-relaxed text-[var(--warning)]">
          You&apos;re viewing <span className="font-semibold">{env}</span>, but these are the
          service-wide variables. Saving here changes every environment, production included.
          To set a value for {env} only, run{" "}
          <code className="font-mono">
            kuso env set {project} {service} KEY=VALUE --env {env}
          </code>
          .
        </p>
      )}

      {overrideVars.length > 0 && (
        <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
          <header className="border-b border-[var(--border-subtle)] px-3 py-2">
            <h4 className="text-xs font-semibold tracking-tight">Overrides on {env}</h4>
            <p className="mt-0.5 text-[10px] text-[var(--text-tertiary)]">
              Read-only here. These win over the service-wide values below, on {env} only.
            </p>
          </header>
          <ul className="divide-y divide-[var(--border-subtle)] font-mono text-[11px]">
            {overrideVars.map((v) => (
              <li key={v.name} className="flex gap-3 px-3 py-1.5">
                <span className="text-[var(--text-primary)]">{v.name}</span>
                <span className="truncate text-[var(--text-secondary)]">
                  {v.valueFrom ? "(secret reference)" : v.value}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

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

      <EnvVarsEditor project={project} service={service} env={env} />

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
