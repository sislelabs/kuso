"use client";

import { useMemo } from "react";
import { countServiceChanges, useStagedProject, useStagedService } from "@/features/staged";
import { cn } from "@/lib/utils";

// useStagedServiceNames lists the services in a project with staged edits.
export function useStagedServiceNames(project: string): Set<string> {
  const staged = useStagedProject(project);
  return useMemo(
    () => new Set(Object.keys(staged ?? {}).filter((s) => countServiceChanges(staged?.[s]) > 0)),
    [staged],
  );
}

// StagedDot: a small accent dot for a canvas node / list row whose service
// has edits waiting for deploy. Renders nothing otherwise.
export function StagedDot({ project, service, className }: { project: string; service: string; className?: string }) {
  const n = countServiceChanges(useStagedService(project, service));
  if (n === 0) return null;
  const label = `${n} staged ${n === 1 ? "change" : "changes"}`;
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      className={cn("inline-block h-2 w-2 shrink-0 rounded-full bg-[var(--accent)] ring-2 ring-[var(--bg-secondary)]", className)}
    />
  );
}
