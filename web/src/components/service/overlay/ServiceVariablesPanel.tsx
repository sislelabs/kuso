"use client";

import { EnvVarsEditor } from "@/components/service/EnvVarsEditor";

export function ServiceVariablesPanel({ project, service }: { project: string; service: string }) {
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
      <EnvVarsEditor project={project} service={service} />
    </div>
  );
}
