"use client";

import type React from "react";
import type { KusoService, KusoVolume } from "@/types/projects";
import { cn } from "@/lib/utils";

// ----- shape of the entire editable surface -----

export interface PlacementRow {
  key: string;
  value: string;
}

export interface VolumeRow {
  name: string;
  mountPath: string;
  sizeGi: number;
}

export interface FormState {
  // Display label (free-form). Empty = canvas/header falls back to
  // the slug.
  displayName: string;
  // Source
  repoURL: string;
  repoBranch: string;
  repoPath: string;
  repoInstallationID: number; // 0 = unchanged / inherit
  // Networking
  port: string;
  domains: string; // newline-separated
  internal: boolean;
  // Scale
  scaleMin: string;
  scaleMax: string;
  scaleCPU: string;
  // Build
  runtime: string;
  // Storage
  volumes: VolumeRow[];
  // Placement
  placement: PlacementRow[];
  placementNodes: string[];
  // Deploy
  previewsDisabled: boolean;
}

export interface SectionProps {
  state: FormState;
  setState: React.Dispatch<React.SetStateAction<FormState>>;
}

export interface NodeSummary {
  name: string;
  ready: boolean;
  region?: string;
  kusoLabels?: Record<string, string>;
}

export const RUNTIMES = ["dockerfile", "nixpacks", "static", "buildpacks"] as const;

// ----- form ⇄ CR mapping -----

export function fromSvc(svc?: KusoService): FormState {
  const repo = svc?.spec.repo;
  // Service-level installationId is on spec.github.installationId; the
  // type may not declare it yet (we ship that as a non-breaking
  // addition), so cast through unknown to read it without forcing a
  // type-system rev.
  const ghSpec = (svc?.spec as { github?: { installationId?: number } } | undefined)?.github;
  return {
    displayName: svc?.spec.displayName ?? "",
    repoURL: repo?.url ?? "",
    repoBranch: repo?.defaultBranch ?? "",
    repoPath: repo?.path && repo.path !== "." ? repo.path : "",
    repoInstallationID: ghSpec?.installationId ?? 0,
    port: String(svc?.spec.port ?? 8080),
    domains: (svc?.spec.domains ?? []).map((d) => d.host ?? "").filter(Boolean).join("\n"),
    internal: !!(svc?.spec as { internal?: boolean } | undefined)?.internal,
    scaleMin: String(svc?.spec.scale?.min ?? 1),
    scaleMax: String(svc?.spec.scale?.max ?? 5),
    scaleCPU: String(svc?.spec.scale?.targetCPU ?? 70),
    runtime: svc?.spec.runtime ?? "dockerfile",
    volumes: (svc?.spec.volumes ?? []).map((v: KusoVolume) => ({
      name: v.name,
      mountPath: v.mountPath,
      sizeGi: v.sizeGi ?? 1,
    })),
    placement: Object.entries(svc?.spec.placement?.labels ?? {}).map(([k, v]) => ({
      key: k,
      value: v,
    })),
    placementNodes: svc?.spec.placement?.nodes ?? [],
    // Per-service preview opt-out lives on spec.previews.disabled. The
    // KusoService type may not declare it (newer field), so cast.
    previewsDisabled:
      !!(svc?.spec as { previews?: { disabled?: boolean } } | undefined)?.previews?.disabled,
  };
}

// shallowEqual compares two form states. We use it to drive the
// "dirty" flag for the floating save bar — JSON.stringify is fine
// for this shape (no Date / Map / Set) and 10× cheaper than a
// hand-rolled walk.
export function isEqual(a: FormState, b: FormState): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

// ----- layout primitives shared by every section -----

export function Section({
  id,
  title,
  icon: Icon,
  children,
  hint,
}: {
  id: string;
  title: string;
  icon: React.ComponentType<{ className?: string }>;
  children: React.ReactNode;
  hint?: string;
}) {
  return (
    <section id={id} className="scroll-mt-6">
      <header className="mb-2 flex items-center gap-2">
        <Icon className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
        <h3 className="font-heading text-sm font-semibold tracking-tight">{title}</h3>
        {hint && (
          <span className="ml-auto font-mono text-[10px] text-[var(--text-tertiary)]">
            {hint}
          </span>
        )}
      </header>
      <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        {children}
      </div>
    </section>
  );
}

// Row is a single one-liner: label on the left, control on the
// right, divider below (unless `last`). The hover background gives
// the row a clickable feel even when only the control is interactive.
export function Row({
  label,
  hint,
  control,
  last,
}: {
  label: string;
  hint?: string;
  control: React.ReactNode;
  last?: boolean;
}) {
  return (
    <div
      className={cn(
        // Phones get a stacked layout (label-then-control) so
        // controls have the full row width to breathe; the side-by-
        // side layout returns at sm and above where there's room.
        "flex flex-col gap-2 px-3 py-2 sm:flex-row sm:items-center sm:gap-3",
        !last && "border-b border-[var(--border-subtle)]",
      )}
    >
      <div className="min-w-0 sm:min-w-[140px]">
        <div className="text-[12px] text-[var(--text-secondary)]">{label}</div>
        {hint && (
          <div className="font-mono text-[10px] text-[var(--text-tertiary)]/70">
            {hint}
          </div>
        )}
      </div>
      <div className="flex min-w-0 flex-1 items-center justify-start gap-2 sm:ml-auto sm:justify-end">
        {control}
      </div>
    </div>
  );
}
