"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { toast } from "sonner";
import { usePatchService, type PatchServiceBody } from "@/features/services";
import { useCan, Perms } from "@/features/auth";
import { useEnvironments, setEnvGroupServiceBranch } from "@/features/projects";
import { useQueryClient } from "@tanstack/react-query";
import type { KusoService } from "@/types/projects";
import { Github, Trash2, Network, Layers3, Hammer, Cloud, HardDrive, MapPin } from "lucide-react";
import { cn } from "@/lib/utils";

import { useOverlayDirty } from "@/components/service/ServiceOverlay";
import { DiffConfirmDialog, type DiffEntry } from "@/components/shared/DiffConfirmDialog";
import { serviceBlast } from "@/lib/blast-radius";
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
  // env-group name from the URL ?env= search param. "production" =
  // hide the env-scoped Branch section (the production branch is set
  // via the regular Source section). Anything else surfaces an inline
  // env-branch control that PATCHes the env CR's spec.branch and lets
  // the user point one service at a different branch within this env.
  env?: string;
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
export function ServiceSettingsPanel({ project, service, svc, env }: Props) {
  const onProduction = !env || env === "production";
  const baseline = useMemo(() => fromSvc(svc), [svc]);
  const [state, setState] = useState<FormState>(baseline);
  const [pending, setPending] = useState(false);
  // saveError surfaces the last save failure inline next to the
  // unsaved-changes pip. Pure-toast errors disappeared too quickly
  // and got buried during traefik flap (a Customer™ literally lost
  // a domain edit because the toast fell off-screen behind a
  // probe-failure stack — see /domains-add-remove-list change).
  const [saveError, setSaveError] = useState<string | null>(null);
  // pendingBody holds a built-but-not-yet-applied patch while the
  // blast-radius confirm dialog is open. null = dialog closed.
  const [pendingBody, setPendingBody] = useState<PatchServiceBody | null>(null);
  const patch = usePatchService(project, service);
  // Pull the production env's host so the Networking section can
  // surface the auto-domain inline (read-only). The KusoService spec
  // doesn't carry the rendered hostname — that's stamped on the
  // KusoEnvironment at create time — so we have to reach over here.
  const envs = useEnvironments(project);
  const autoHost = useMemo(() => {
    const list = envs.data ?? [];
    const prod = list.find(
      (e) =>
        e.spec.service === service ||
        e.spec.service === `${project}-${service}`,
    );
    return prod?.spec.host;
  }, [envs.data, project, service]);
  // Gate the floating save bar on services:write — viewers can scroll
  // through the panel but can't edit. Inputs are still editable to
  // preserve copy/paste affordance, just not committable.
  const canWrite = useCan(Perms.ServicesWrite);

  // Whenever the upstream service changes (refetch lands fresh data),
  // re-baseline so the dirty flag clears. We only do this when the
  // user has no in-flight edits — otherwise their typing would get
  // clobbered by a refetch.
  // Ref carries the previous baseline across renders so we can ask
  // "has the user edited yet" without putting `state` in the deps.
  const prevBaselineRef = useRef<FormState>(baseline);
  useEffect(() => {
    setState((prev) => (isEqual(prev, prevBaselineRef.current) ? baseline : prev));
    prevBaselineRef.current = baseline;
  }, [baseline]);

  const dirty = !isEqual(state, baseline);

  const onSave = async () => {
    const body: PatchServiceBody = {};
    if (state.displayName !== baseline.displayName) {
      const trimmed = state.displayName.trim();
      // Hyphen at end of class doesn't need escaping (eslint
      // no-useless-escape).
      if (trimmed && !/^[A-Za-z0-9 -]{1,60}$/.test(trimmed)) {
        toast.error("Display name: letters/digits/spaces/hyphens only, ≤60 chars");
        return;
      }
      body.displayName = trimmed;
    }
    const portNum = Number(state.port);
    if (portNum !== Number(baseline.port)) {
      if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
        toast.error("Port must be 1–65535");
        return;
      }
      body.port = portNum;
    }
    {
      // Compare normalised forms (trimmed + empty-filtered) so that
      // adding/removing an empty editor row doesn't flip the form
      // to "dirty" with no real change. The textarea-row UI renders
      // an empty row at the bottom for the user to type into; we
      // don't want that to fight the Save bar.
      const norm = (s: string) =>
        s.split("\n").map((x) => x.trim()).filter(Boolean).join("\n");
      const a = norm(state.domains);
      const b = norm(baseline.domains);
      if (a !== b) {
        body.domains = a
          .split("\n")
          .filter(Boolean)
          .map((host) => ({ host, tls: true }));
      }
    }
    if (state.internal !== baseline.internal) {
      body.internal = state.internal;
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
      // Only flip the sleep enabled flag — keep the user's existing
      // afterMinutes value. Pre-v0.10 we hardcoded afterMinutes: 5
      // on every scale save, silently resetting any custom idle
      // timeout the user had configured elsewhere.
      body.sleep = { enabled: min === 0 };
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

    if (Object.keys(body).length === 0) {
      // Nothing actually changed (user shuffled empty rows around or
      // typed-then-deleted). Reset the baseline so the save bar
      // hides without firing a no-op API call.
      setState(baseline);
      setSaveError(null);
      return;
    }

    // Don't patch straight away — open the blast-radius confirm
    // dialog so the user sees what each changed field does to the
    // running workload (rolling restart, TLS re-issue, data orphan…)
    // before committing. applyPatch (the dialog's confirm) does the
    // actual mutation.
    setSaveError(null);
    setPendingBody(body);
  };

  // applyPatch commits the body the confirm dialog is showing.
  const applyPatch = async (body: PatchServiceBody) => {
    setPending(true);
    setSaveError(null);
    try {
      await patch.mutateAsync(body);
      toast.success("Changes saved");
      setPendingBody(null);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "Failed to save";
      // Both surfaces: toast for momentary visibility, inline
      // saveError for "where did my changes go" recovery.
      toast.error(msg);
      setSaveError(msg);
      setPendingBody(null);
    } finally {
      setPending(false);
    }
  };

  // diffEntries turns the pending patch body into the confirm
  // dialog's row list, each tagged with its EDIT_SAFETY blast radius.
  const diffEntries: DiffEntry[] = pendingBody
    ? Object.keys(pendingBody).map((field) => ({
        field,
        before: "current",
        after: "changed",
        warning: serviceBlast(field) ?? undefined,
      }))
    : [];

  const reset = () => {
    setState(baseline);
    setSaveError(null);
  };

  // Register dirty + save with the overlay shell so the unified
  // SaveBar (rendered in ServiceOverlay.tsx) fires onSave for this
  // panel. The inline FloatingSaveBar below stays for the
  // read-only "your role can't edit" affordance, but the dirty
  // case is handled by the shell now.
  useOverlayDirty("settings", dirty && canWrite, {
    onSave,
    onDiscard: reset,
    saving: pending,
    saveError: saveError ?? undefined,
  });

  return (
    <div className="relative">
      {/* On md+ the layout is a 2-col grid with a sticky sidebar
          jump-nav. On smaller screens the sidebar would steal half
          the width from the actual form, so we collapse to a single
          column + a horizontal-scroll chip strip pinned to the top.
          That keeps the section anchors discoverable on phones
          without crowding the inputs. */}
      <nav className="sticky top-0 z-10 -mx-px flex gap-1 overflow-x-auto border-b border-[var(--border-subtle)] bg-[var(--bg-primary)]/95 px-3 py-2 text-xs backdrop-blur md:hidden">
        {SECTIONS.map((s) => (
          <a
            key={s.id}
            href={`#${s.id}`}
            className={cn(
              "inline-flex shrink-0 items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1 text-[var(--text-tertiary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)]",
              s.id === "danger" && "text-red-400/70 hover:text-red-400",
            )}
          >
            <s.icon className="h-3 w-3" />
            {s.label}
          </a>
        ))}
      </nav>
      <div className="grid grid-cols-1 gap-0 pb-24 md:grid-cols-[1fr_180px]">
        <div className="space-y-8 px-4 py-4 md:px-6 md:py-6">
          {!onProduction && env && (
            <EnvBranchSection project={project} env={env} service={service} svc={svc} />
          )}
          <SourceSection state={state} setState={setState} project={project} service={service} />
          <NetworkingSection state={state} setState={setState} autoHost={autoHost} />
          <ScaleSection state={state} setState={setState} />
          <PlacementSection state={state} setState={setState} />
          <VolumesSection state={state} setState={setState} />
          <BuildSection state={state} setState={setState} />
          <DeploySection project={project} state={state} setState={setState} />
          <DangerSection project={project} service={service} />
        </div>

        <nav className="sticky top-0 hidden self-start px-4 py-6 text-sm md:block">
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

      {/* The dirty/save UX is now handled by the unified SaveBar
          in ServiceOverlay.tsx via useOverlayDirty's onSave hook.
          We only keep the inline "read-only" hint for users whose
          role can't commit — they need an explanation, not a
          disabled button. */}
      {saveError && (
        <div className="sticky bottom-16 z-20 mx-4 flex items-center justify-end">
          <span className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 font-mono text-[10px] text-red-300 shadow-[var(--shadow-md)]">
            {saveError}
          </span>
        </div>
      )}
      {dirty && !canWrite && (
        <div className="sticky bottom-4 z-20 mx-4 flex items-center justify-end">
          <span className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-3 py-2 font-mono text-[10px] text-[var(--text-tertiary)] shadow-[var(--shadow-md)]">
            read-only — your role can&apos;t edit services
          </span>
        </div>
      )}
      <DiffConfirmDialog
        open={pendingBody !== null}
        title="Apply service changes?"
        description="Review the blast radius of each change before it reconciles."
        entries={diffEntries}
        confirmLabel="Apply & reconcile"
        confirming={pending}
        onCancel={() => setPendingBody(null)}
        onConfirm={() => {
          if (pendingBody) void applyPatch(pendingBody);
        }}
      />
    </div>
  );
}

// (FloatingSaveBar removed — the overlay shell renders a unified
// SaveBar from useOverlayDirty's onSave hook now.)

// EnvBranchSection is the per-(env, service) branch override surface.
// Only rendered for non-production envs; the production branch is set
// via the regular Source section below (which writes to the
// KusoService spec). For env-cloned services, branch is on the env CR
// — kuso patches it via PATCH /api/projects/{p}/env-groups/{env}/
// services/{service}/branch and the build poller picks up new pushes
// to that branch as redeploys.
function EnvBranchSection({
  project,
  env,
  service,
  svc,
}: {
  project: string;
  env: string;
  service: string;
  svc?: KusoService;
}) {
  // Pull the current branch from the env CR (production's env CR for
  // this service, narrowed by env-name label). Falls back to the
  // service's repo default branch as a hint.
  const envs = useEnvironments(project);
  const fqn = `${project}-${service}`;
  const envRow = (envs.data ?? []).find(
    (e) =>
      e.spec.service === fqn &&
      (e.metadata.labels?.["kuso.sislelabs.com/env"] ?? "") === env,
  );
  const currentBranch =
    envRow?.spec.branch ??
    svc?.spec?.repo?.defaultBranch ??
    "";
  const repoLabel = (() => {
    const url = svc?.spec?.repo?.url ?? "";
    if (!url) return "";
    const m = url.match(/github\.com[/:]([^/]+\/[^/.]+)/i);
    return m ? m[1] : url;
  })();
  const [branch, setBranch] = useState(currentBranch);
  useEffect(() => {
    setBranch(currentBranch);
  }, [currentBranch]);
  const dirty = branch.trim() !== "" && branch !== currentBranch;
  const [saving, setSaving] = useState(false);
  const qc = useQueryClient();
  const save = async () => {
    setSaving(true);
    try {
      await setEnvGroupServiceBranch(project, env, service, branch.trim());
      toast.success(
        `${service} in ${env} now tracks ${branch.trim()} — push to that branch to redeploy.`,
      );
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
      qc.invalidateQueries({ queryKey: ["projects", project, "env-groups"] });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };
  return (
    <section className="rounded-md border border-blue-500/30 bg-blue-500/5">
      <header className="border-b border-blue-500/20 px-3 py-2">
        <h3 className="text-sm font-medium">
          Branch in <span className="font-mono text-blue-200">{env}</span>
        </h3>
        <p className="mt-0.5 text-[11px] text-[var(--text-secondary)]">
          {repoLabel ? (
            <>
              Branch of <span className="font-mono">{repoLabel}</span> that this service tracks
              within <span className="font-mono">{env}</span>. Production keeps using its own
              default-branch setting; this override is env-scoped.
            </>
          ) : (
            <>
              Branch this service tracks within{" "}
              <span className="font-mono">{env}</span>. Doesn&apos;t affect production.
            </>
          )}
        </p>
      </header>
      <div className="flex items-center gap-2 p-3">
        <input
          type="text"
          value={branch}
          onChange={(e) => setBranch(e.target.value)}
          placeholder={svc?.spec?.repo?.defaultBranch || "main"}
          spellCheck={false}
          className="h-8 flex-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px] outline-none focus:border-[var(--accent)]"
        />
        <Button size="sm" disabled={!dirty || saving} onClick={save}>
          {saving ? "Saving…" : "Save branch"}
        </Button>
      </div>
    </section>
  );
}
