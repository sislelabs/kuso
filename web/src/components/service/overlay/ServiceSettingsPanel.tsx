"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { Button } from "@/components/ui/button";
import { toast } from "sonner";
import { usePatchService, type PatchServiceBody } from "@/features/services";
import { useCan, Perms } from "@/features/auth";
import type { KusoService } from "@/types/projects";
import { Github, Trash2, Network, Layers3, Hammer, Cloud, Save, HardDrive, MapPin } from "lucide-react";
import { cn } from "@/lib/utils";

import { fromSvc, isEqual, type FormState } from "./settings/_primitives";
import { SourceSection } from "./settings/SourceSection";
import { NetworkingSection } from "./settings/NetworkingSection";
import { ScaleSection } from "./settings/ScaleSection";
import { PlacementSection } from "./settings/PlacementSection";
import { VolumesSection } from "./settings/VolumesSection";
import { BuildSection } from "./settings/BuildSection";
import { DeploySection } from "./settings/DeploySection";
import { DangerSection } from "./settings/DangerSection";

interface Props {
  project: string;
  service: string;
  svc?: KusoService;
}

const SECTIONS = [
  { id: "source",     label: "Source",     icon: Github },
  { id: "networking", label: "Networking", icon: Network },
  { id: "scale",      label: "Scale",      icon: Layers3 },
  { id: "placement",  label: "Placement",  icon: MapPin },
  { id: "volumes",    label: "Volumes",    icon: HardDrive },
  { id: "build",      label: "Build",      icon: Hammer },
  { id: "deploy",     label: "Deploy",     icon: Cloud },
  { id: "danger",     label: "Danger",     icon: Trash2 },
] as const;

// ServiceSettingsPanel orchestrates the per-service settings overlay.
// Each section lives in ./settings/<Name>Section.tsx; this file owns
// the form state, the dirty/save bar, and the section-anchor nav.
export function ServiceSettingsPanel({ project, service, svc }: Props) {
  const baseline = useMemo(() => fromSvc(svc), [svc]);
  const [state, setState] = useState<FormState>(baseline);
  const [pending, setPending] = useState(false);
  const patch = usePatchService(project, service);
  // Gate the floating save bar on services:write — viewers can scroll
  // through the panel but can't edit. Inputs are still editable to
  // preserve copy/paste affordance, just not committable.
  const canWrite = useCan(Perms.ServicesWrite);

  // Whenever the upstream service changes (refetch lands fresh data),
  // re-baseline so the dirty flag clears. We only do this when the
  // user has no in-flight edits — otherwise their typing would get
  // clobbered by a refetch.
  const prevBaselineRef = useRef<FormState>(baseline);
  useEffect(() => {
    if (isEqual(state, prevBaselineRef.current)) {
      setState(baseline);
    }
    prevBaselineRef.current = baseline;
  }, [baseline]);

  const dirty = !isEqual(state, baseline);

  const onSave = async () => {
    const body: PatchServiceBody = {};
    const portNum = Number(state.port);
    if (portNum !== Number(baseline.port)) {
      if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
        toast.error("Port must be 1–65535");
        return;
      }
      body.port = portNum;
    }
    if (state.domains !== baseline.domains) {
      body.domains = state.domains
        .split("\n")
        .map((s) => s.trim())
        .filter(Boolean)
        .map((host) => ({ host, tls: true }));
    }
    if (
      state.scaleMin !== baseline.scaleMin ||
      state.scaleMax !== baseline.scaleMax ||
      state.scaleCPU !== baseline.scaleCPU
    ) {
      const min = Number(state.scaleMin);
      const max = Number(state.scaleMax);
      const cpu = Number(state.scaleCPU);
      if (min < 0 || max < Math.max(min, 1)) {
        toast.error("max must be ≥ max(min, 1) and min ≥ 0");
        return;
      }
      body.scale = { min, max, targetCPU: cpu };
      body.sleep = { enabled: min === 0, afterMinutes: 5 };
    }
    if (state.runtime !== baseline.runtime) {
      body.runtime = state.runtime;
    }
    if (
      state.repoURL !== baseline.repoURL ||
      state.repoBranch !== baseline.repoBranch ||
      state.repoPath !== baseline.repoPath ||
      state.repoInstallationID !== baseline.repoInstallationID
    ) {
      body.repo = {
        url: state.repoURL,
        branch: state.repoBranch || undefined,
        path: state.repoPath || undefined,
        // 0 → omit so the server doesn't clobber the existing
        // installationId with "unset"; only send when explicitly
        // changed by the picker.
        installationId: state.repoInstallationID || undefined,
      };
    }
    const pNow = JSON.stringify({ p: state.placement, n: state.placementNodes });
    const pBase = JSON.stringify({ p: baseline.placement, n: baseline.placementNodes });
    if (pNow !== pBase) {
      const labels: Record<string, string> = {};
      for (const r of state.placement) {
        if (!r.key.trim()) continue;
        labels[r.key.trim()] = r.value;
      }
      const nodes = state.placementNodes.filter(Boolean);
      // Sending an empty placement {} explicitly clears it; we only
      // want to do that when the user actually had something set
      // before. Otherwise nil-vs-{} gets ambiguous server-side.
      if (Object.keys(labels).length === 0 && nodes.length === 0) {
        body.placement = { clear: true };
      } else {
        body.placement = { labels, nodes };
      }
    }
    const vNow = JSON.stringify(state.volumes);
    const vBase = JSON.stringify(baseline.volumes);
    if (vNow !== vBase) {
      for (const v of state.volumes) {
        if (!v.name || !v.mountPath) continue;
        if (!/^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$/.test(v.name)) {
          toast.error(`Volume name "${v.name}": lowercase, dashes, ≤32 chars`);
          return;
        }
        if (!v.mountPath.startsWith("/")) {
          toast.error(`Mount path "${v.mountPath}" must start with /`);
          return;
        }
      }
      body.volumes = state.volumes.filter((v) => v.name && v.mountPath);
    }
    if (state.previewsDisabled !== baseline.previewsDisabled) {
      // Send {disabled} when the user opted-out, or {clear:true} when
      // they re-enabled (drops the override so the service falls back
      // to the project toggle's setting).
      body.previews = state.previewsDisabled ? { disabled: true } : { clear: true };
    }

    setPending(true);
    try {
      await patch.mutateAsync(body);
      toast.success("Changes saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save");
    } finally {
      setPending(false);
    }
  };

  const reset = () => setState(baseline);

  return (
    <div className="relative">
      <div className="grid grid-cols-[1fr_180px] gap-0 pb-20">
        <div className="space-y-8 px-6 py-6">
          <SourceSection state={state} setState={setState} project={project} service={service} />
          <NetworkingSection state={state} setState={setState} />
          <ScaleSection state={state} setState={setState} />
          <PlacementSection state={state} setState={setState} />
          <VolumesSection state={state} setState={setState} />
          <BuildSection state={state} setState={setState} />
          <DeploySection project={project} state={state} setState={setState} />
          <DangerSection project={project} service={service} />
        </div>

        <nav className="sticky top-0 self-start px-4 py-6 text-sm">
          <ul className="space-y-2">
            {SECTIONS.map((s) => (
              <li key={s.id}>
                <a
                  href={`#${s.id}`}
                  className={cn(
                    "flex items-center gap-2 rounded px-2 py-1 text-[var(--text-tertiary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)] transition-colors",
                    s.id === "danger" && "text-red-400/70 hover:text-red-400",
                  )}
                >
                  <s.icon className="h-3 w-3" />
                  {s.label}
                </a>
              </li>
            ))}
          </ul>
        </nav>
      </div>

      {/* Floating save bar — slides up from bottom-right when ANY
          field is dirty. Sticks to the overlay's right edge so it
          stays visible while the user scrolls through sections.
          Gated by services:write — viewers can flip switches in
          their browser but can't commit. */}
      <FloatingSaveBar dirty={dirty && canWrite} pending={pending} onSave={onSave} onReset={reset} />
      {dirty && !canWrite && (
        <div className="sticky bottom-4 z-20 mx-4 flex items-center justify-end">
          <span className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-3 py-2 font-mono text-[10px] text-[var(--text-tertiary)] shadow-[var(--shadow-md)]">
            read-only — your role can&apos;t edit services
          </span>
        </div>
      )}
    </div>
  );
}

function FloatingSaveBar({
  dirty,
  pending,
  onSave,
  onReset,
}: {
  dirty: boolean;
  pending: boolean;
  onSave: () => void;
  onReset: () => void;
}) {
  // Layout: "unsaved changes" anchored left so the user reads
  // status first; Discard + Save on the right with Discard as a
  // proper outline button (was an underline-text affordance —
  // invisible in dark mode unless you knew where to look).
  return (
    <AnimatePresence>
      {dirty && (
        <motion.div
          initial={{ y: 60, opacity: 0 }}
          animate={{ y: 0, opacity: 1 }}
          exit={{ y: 60, opacity: 0 }}
          transition={{ type: "spring", stiffness: 360, damping: 32 }}
          className="sticky bottom-4 z-20 mx-4 flex items-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-3 py-2 shadow-[var(--shadow-lg)]"
        >
          <span className="mr-auto inline-flex items-center gap-1.5 font-mono text-[10px] text-[var(--text-tertiary)]">
            <span className="inline-block h-1.5 w-1.5 rounded-full bg-amber-400" />
            unsaved changes
          </span>
          <Button size="sm" variant="outline" onClick={onReset} disabled={pending}>
            Discard
          </Button>
          <Button size="sm" onClick={onSave} disabled={pending}>
            <Save className="h-3 w-3" />
            {pending ? "Saving…" : "Save changes"}
          </Button>
        </motion.div>
      )}
    </AnimatePresence>
  );
}
