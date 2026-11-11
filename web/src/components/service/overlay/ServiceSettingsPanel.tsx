"use client";

import { smoothScroll } from "@/lib/reduced-motion";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toast } from "sonner";
import { describeError, toastError } from "@/lib/errors";
import { usePatchService, type PatchServiceBody } from "@/features/services";
import { useCanOnProject, Perms } from "@/features/auth";
import { useEnvironments, setEnvGroupServiceBranch } from "@/features/projects";
import { useQueryClient } from "@tanstack/react-query";
import { stripRepoCredentials } from "@/lib/format";
import { envGroupName } from "@/lib/env-group";
import type { KusoService } from "@/types/projects";
import {
  Github,
  Trash2,
  Network,
  Layers3,
  Hammer,
  Cloud,
  HardDrive,
  MapPin,
  ShieldAlert,
  Rocket,
  Moon,
  Activity,
  Globe,
  GitBranch,
  HeartPulse,
  BellRing,
  Puzzle,
} from "lucide-react";
import { cn } from "@/lib/utils";

import { useOverlayDirty } from "@/components/service/ServiceOverlay";
import { DiffConfirmDialog, type DiffEntry } from "@/components/shared/DiffConfirmDialog";
import { serviceBlast } from "@/lib/blast-radius";
import {
  Code,
  Section,
  SettingsSaveProvider,
  fromSvc,
  monoInputClass,
  type FormState,
  type SettingsSaveApi,
} from "./settings/_primitives";
import { buildServicePatch, fieldDiffValues } from "./settings/buildServicePatch";
import {
  SECTION_FIELDS,
  dirtySections,
  discardSection,
  isSaveableSection,
  scopedState,
  type SaveableSection,
} from "./settings/sectionFields";
import { getStagedState, stageSettings, useStagedService } from "@/features/staged";
import { StagedChangesBar } from "@/components/staged/StagedChangesBar";
import { overlaySettings } from "@/components/staged/plan";
import { SourceSection } from "./settings/SourceSection";
import { DomainsCard, useCertIssuer } from "./settings/DomainsCard";
import { IntegrationsSection } from "./settings/IntegrationsSection";
import { NetworkingSection } from "./settings/NetworkingSection";
import { ScaleSection } from "./settings/ScaleSection";
import { SleepSection } from "./settings/SleepSection";
import { PlacementSection } from "./settings/PlacementSection";
import { VolumesSection } from "./settings/VolumesSection";
import { BuildSection } from "./settings/BuildSection";
import { AlertsSection } from "./settings/AlertsSection";
import { DeploySection } from "./settings/DeploySection";
import { ReleaseSection } from "./settings/ReleaseSection";
import { UptimeSection } from "./settings/UptimeSection";
import { HealthcheckSection } from "./settings/HealthcheckSection";
import { SecuritySection } from "./settings/SecuritySection";
import { DangerSection } from "./settings/DangerSection";
import { findSettingsFields, revealSettingsTarget, type SettingsFieldHit } from "./settings/reveal";

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
  { id: "source", label: "Source", icon: Github },
  { id: "domains", label: "Domains", icon: Globe },
  { id: "networking", label: "Networking", icon: Network },
  { id: "scale", label: "Scale", icon: Layers3 },
  { id: "sleep", label: "Sleep", icon: Moon },
  { id: "placement", label: "Placement", icon: MapPin },
  { id: "volumes", label: "Volumes", icon: HardDrive },
  { id: "build", label: "Build", icon: Hammer },
  { id: "deploy", label: "Deploy", icon: Cloud },
  { id: "release", label: "Release", icon: Rocket },
  { id: "healthcheck", label: "Health check", icon: HeartPulse },
  { id: "uptime", label: "Uptime", icon: Activity },
  { id: "alerts", label: "Alerts", icon: BellRing },
  { id: "integrations", label: "Integrations", icon: Puzzle },
  { id: "security", label: "Security", icon: ShieldAlert },
  { id: "danger", label: "Danger zone", icon: Trash2 },
] as const;

// Words people search for that aren't a section's name.
const SECTION_KEYWORDS: Record<string, string> = {
  source: "repo repository branch git github gitlab token root directory monorepo watch paths deploy hook slug rename display name",
  domains: "domain hostname dns tls https certificate url",
  networking: "port internal public expose protocol",
  scale: "replicas autoscale cpu memory size resources instances",
  sleep: "idle scale to zero wake",
  placement: "node region label pin",
  volumes: "disk storage persistent mount pvc",
  build: "dockerfile nixpacks buildpacks runtime build args command static",
  deploy: "auto-deploy push start command cmd ci checks previews pull request",
  release: "migration release hook pre-deploy command",
  healthcheck: "readiness liveness probe path",
  uptime: "monitor ping check",
  security: "capabilities privilege root security context",
  alerts: "alert rule notify notification page error rate latency",
  danger: "delete remove",
};

function sectionMatches(id: string, label: string, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return `${label} ${SECTION_KEYWORDS[id] ?? ""}`.toLowerCase().includes(q);
}

const SECTION_LABEL: Record<string, string> = Object.fromEntries(SECTIONS.map((s) => [s.id, s.label]));
const SAVED_FLASH_MS = 3000;
// Cards that save on their own instead of staging: removing a volume
// orphans its data, so it keeps its own confirm and applies at once.
const IMMEDIATE: readonly SaveableSection[] = ["volumes"];
const IMMEDIATE_FIELDS = new Set<string>(IMMEDIATE.flatMap((s) => SECTION_FIELDS[s] as readonly string[]));

interface PendingSave {
  sections: SaveableSection[];
  target: FormState;
  body: PatchServiceBody;
}

function sameValue(a: unknown, b: unknown): boolean {
  return a === b || JSON.stringify(a) === JSON.stringify(b);
}

// ServiceSettingsPanel orchestrates the per-service settings tab. Edits
// to most cards are staged (features/staged) and deploy together from the
// StagedChangesBar, so a service rolls once however many cards changed.
// Volumes still save per card. Each card lives in ./settings/; this file
// owns the form state, the staging and the section nav.
export function ServiceSettingsPanel({ project, service, svc, env }: Props) {
  const onProduction = !env || env === "production";
  const envName = env || "production";
  // useEnvironments is cached + shared with the canvas, so this doesn't
  // add a round-trip. Domains and the env branch live on the env CR.
  const envs = useEnvironments(project);
  const activeEnv = useMemo(() => {
    const list = envs.data ?? [];
    // Group identity is the env label (unlabelled legacy = spec.kind).
    // No cross-group fallback: another group's env would let domain
    // edits land on the wrong env.
    return list.find(
      (e) =>
        (e.spec.service === service || e.spec.service === `${project}-${service}`) && envGroupName(e) === envName,
    );
  }, [envs.data, project, service, envName]);

  const serviceEnvCount = useMemo(
    () =>
      (envs.data ?? []).filter((e) => e.spec.service === service || e.spec.service === `${project}-${service}`).length,
    [envs.data, project, service],
  );
  const baseline = useMemo(() => fromSvc(svc), [svc]);
  const staged = useStagedService(project, service);
  // Immediate-card edits (volumes) stay local: they're unsaved, not staged.
  const [local, setLocal] = useState<Partial<FormState>>({});
  const state = useMemo<FormState>(
    () => ({ ...overlaySettings(baseline, staged?.settings?.fields), ...local }),
    [baseline, staged?.settings?.fields, local],
  );
  const baselineRef = useRef(baseline);
  baselineRef.current = baseline;
  const localRef = useRef(local);
  localRef.current = local;
  const [saving, setSaving] = useState<SaveableSection[]>([]);
  const [errors, setErrors] = useState<Partial<Record<SaveableSection, string>>>({});
  const [savedAt, setSavedAt] = useState<Partial<Record<SaveableSection, number>>>({});
  const [pendingSave, setPendingSave] = useState<PendingSave | null>(null);
  const patch = usePatchService(project, service);
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  // Command, image, release hook and build command/images run code with
  // the service's secrets; the server takes the project admin role for them.
  const canExec = useCanOnProject(project, Perms.SecretsRead);

  // setState keeps the cards' React setState signature. It reads the
  // latest staged fields from the store (not a render snapshot) so two
  // updates in one tick compose, then splits the result: immediate
  // fields into local state, everything else into the staged store as
  // overrides of the deployed value.
  const setState = useCallback<React.Dispatch<React.SetStateAction<FormState>>>(
    (update) => {
      const base = baselineRef.current;
      const cur = getStagedState()[project]?.[service]?.settings;
      const current: FormState = { ...overlaySettings(base, cur?.fields), ...localRef.current };
      const next = typeof update === "function" ? update(current) : update;
      const fields: Record<string, unknown> = {};
      const stagedBase: Record<string, unknown> = {};
      const nextLocal: Partial<FormState> = {};
      for (const key of Object.keys(next) as (keyof FormState)[]) {
        if (sameValue(next[key], base[key])) continue;
        if (IMMEDIATE_FIELDS.has(key)) {
          (nextLocal as Record<string, unknown>)[key] = next[key];
          continue;
        }
        fields[key] = next[key];
        stagedBase[key] = cur?.base && key in cur.base ? cur.base[key] : base[key];
      }
      localRef.current = nextLocal;
      setLocal(nextLocal);
      stageSettings(project, service, { fields, base: stagedBase });
    },
    [project, service],
  );

  const dirty = useMemo(() => dirtySections(state, baseline), [state, baseline]);
  const immediateDirty = dirty.filter((d) => IMMEDIATE.includes(d));
  const stagedError = staged?.errors?.settings;

  // Clear the "Saved" flash after a few seconds.
  useEffect(() => {
    const ids = Object.keys(savedAt) as SaveableSection[];
    if (ids.length === 0) return;
    const t = window.setTimeout(() => setSavedAt({}), SAVED_FLASH_MS);
    return () => window.clearTimeout(t);
  }, [savedAt]);

  // Only immediate cards save from here; the rest deploy from the bar.
  const requestSave = useCallback(
    (sections: SaveableSection[]) => {
      const target = scopedState(sections, state, baseline);
      const res = buildServicePatch(target, baseline, {
        command: svc?.spec.command,
        releaseCommand: svc?.spec.release?.command,
      });
      if ("error" in res) {
        setErrors((e) => ({ ...e, ...Object.fromEntries(sections.map((s) => [s, res.error])) }));
        toast.error(res.error);
        return;
      }
      setErrors((e) => {
        const next = { ...e };
        for (const s of sections) delete next[s];
        return next;
      });
      if (Object.keys(res.body).length === 0) {
        setLocal({});
        return;
      }
      // Open the blast-radius dialog first; applyPatch does the write.
      setPendingSave({ sections, target, body: res.body });
    },
    [state, baseline, svc],
  );

  const applyPatch = async ({ sections, body }: PendingSave) => {
    setSaving(sections);
    try {
      await patch.mutateAsync(body);
      setLocal({});
      setSavedAt(Object.fromEntries(sections.map((s) => [s, Date.now()])));
      toast.success(sections.length === 1 ? `${SECTION_LABEL[sections[0]]} saved` : "Changes saved");
    } catch (e) {
      const msg = describeError(e, "Failed to save");
      toastError(e, "Failed to save");
      setErrors((prev) => ({ ...prev, ...Object.fromEntries(sections.map((s) => [s, msg])) }));
    } finally {
      setPendingSave(null);
      setSaving([]);
    }
  };

  const discardSectionEdits = useCallback(
    (id: SaveableSection) => {
      setState((s) => discardSection(id, s, baselineRef.current));
      setErrors((e) => {
        const next = { ...e };
        delete next[id];
        return next;
      });
    },
    [setState],
  );

  // Staged edits survive closing the panel, so the leave guard only
  // covers an unsaved immediate card.
  useOverlayDirty("settings", immediateDirty.length > 0 && canWrite, {
    onDiscard: () => {
      setLocal({});
      setErrors({});
    },
  });

  const saveApi = useMemo<SettingsSaveApi>(
    () => ({
      canWrite,
      status: (id) => {
        if (!isSaveableSection(id)) return null;
        const immediate = IMMEDIATE.includes(id);
        const isDirty = dirty.includes(id);
        return {
          mode: immediate ? "immediate" : "staged",
          dirty: isDirty,
          saving: saving.includes(id),
          saved: savedAt[id] !== undefined,
          error: errors[id] ?? (!immediate && isDirty ? (stagedError ?? null) : null),
        };
      },
      save: (id) => {
        if (isSaveableSection(id) && IMMEDIATE.includes(id)) requestSave([id]);
      },
      discard: (id) => {
        if (isSaveableSection(id)) discardSectionEdits(id);
      },
    }),
    [canWrite, dirty, saving, savedAt, errors, stagedError, requestSave, discardSectionEdits],
  );

  const diffEntries: DiffEntry[] = pendingSave
    ? Object.keys(pendingSave.body).map((field) => {
        const [before, after] = fieldDiffValues(field, baseline, pendingSave.target);
        return { field, before, after, warning: serviceBlast(field) ?? undefined };
      })
    : [];

  // Uptime checks ping the HTTP port, so workers and internal-only
  // services have nothing to check. Domains need a public ingress.
  const uptimeApplies = state.runtime !== "worker" && !state.internal;
  const domainsApply = !baseline.internal;
  // Workers have no port, so the chart renders no probes at all.
  const healthcheckApplies = state.runtime !== "worker";
  // Sign-in helpers need a public URL for the OAuth callback.
  const integrationsApply = state.runtime !== "worker" && !baseline.internal;
  const sections = SECTIONS.filter(
    (s) =>
      (s.id !== "uptime" || uptimeApplies) &&
      (s.id !== "integrations" || integrationsApply) &&
      (s.id !== "domains" || domainsApply) &&
      (s.id !== "healthcheck" || healthcheckApplies),
  );

  const certIssuer = useCertIssuer(project, service, envName, domainsApply && !!activeEnv);
  const rootRef = useRef<HTMLDivElement>(null);
  const active = useScrollSpy(rootRef, sections.map((s) => s.id).join(","));
  // Section jumps move focus to the section heading (not just the
  // viewport), so the next Tab enters that card.
  const jump = (id: string) => {
    revealSettingsTarget(id);
  };
  const [find, setFind] = useState("");
  // Field matches come from the rendered labels, so a search lands on
  // the field where it actually is.
  const [fieldHits, setFieldHits] = useState<SettingsFieldHit[]>([]);
  useEffect(() => {
    const root = rootRef.current;
    setFieldHits(root ? findSettingsFields(root, find).slice(0, 8) : []);
  }, [find]);
  const hitSections = new Set(fieldHits.map((h) => h.section));
  const railSections = sections.filter((s) => sectionMatches(s.id, s.label, find) || hitSections.has(s.id));
  const findFirst = () => {
    if (fieldHits[0]) revealSettingsTarget(fieldHits[0].target);
    else if (railSections[0]) jump(railSections[0].id);
  };

  // A #hash on load (a deep link like #branch) reveals that target once
  // the cards have rendered.
  useEffect(() => {
    const reveal = () => {
      const id = decodeURIComponent(window.location.hash.slice(1));
      if (!id || !rootRef.current?.querySelector(`[id="${CSS.escape(id)}"]`)) return;
      window.setTimeout(() => revealSettingsTarget(id), 150);
    };
    reveal();
    window.addEventListener("hashchange", reveal);
    return () => window.removeEventListener("hashchange", reveal);
  }, []);

  return (
    <SettingsSaveProvider value={saveApi}>
      <div ref={rootRef} className="relative">
        <MobileSectionNav sections={sections} active={active} dirty={dirty} onJump={jump} />
        <div className="grid grid-cols-1 gap-0 pb-28 md:grid-cols-[minmax(0,1fr)_168px]">
          <div className="min-w-0 space-y-6 px-3 py-4 sm:px-4 md:px-6 md:py-6">
            {!onProduction && env && <EnvBranchSection project={project} env={env} service={service} svc={svc} />}
            <SourceSection
              state={state}
              setState={setState}
              project={project}
              service={service}
              deployedRepoURL={baseline.repoURL}
            />
            {domainsApply && (
              <DomainsCard
                project={project}
                service={service}
                envName={envName}
                env={activeEnv}
                envLoading={envs.isLoading}
                canWrite={canWrite}
              />
            )}
            <NetworkingSection state={state} setState={setState} certIssuer={certIssuer} />
            <ScaleSection state={state} setState={setState} environments={serviceEnvCount} />
            <SleepSection state={state} setState={setState} />
            <PlacementSection state={state} setState={setState} />
            <VolumesSection state={state} setState={setState} existingVolumes={baseline.volumes} />
            <BuildSection state={state} setState={setState} project={project} canExec={canExec} />
            <DeploySection project={project} service={service} state={state} setState={setState} canExec={canExec} />
            <ReleaseSection state={state} setState={setState} canExec={canExec} />
            {healthcheckApplies && (
              <HealthcheckSection state={state} setState={setState} project={project} service={service} env={envName} />
            )}
            {uptimeApplies && <UptimeSection project={project} state={state} setState={setState} />}
            <AlertsSection project={project} service={service} env={envName} svc={svc} />
            {integrationsApply && (
              <IntegrationsSection project={project} service={service} host={activeEnv?.spec.host ?? ""} canWrite={canWrite} />
            )}
            <SecuritySection state={state} setState={setState} />
            <DangerSection
              project={project}
              service={service}
              canWrite={canWrite}
              env={
                !onProduction &&
                env &&
                activeEnv &&
                activeEnv.metadata.labels?.["kuso.sislelabs.com/env"] === env
                  ? { group: env, crName: activeEnv.metadata.name }
                  : undefined
              }
            />
          </div>

          <nav aria-label="Settings sections" className="sticky top-0 hidden self-start py-6 pr-4 md:block">
            <Input
              type="search"
              value={find}
              onChange={(e) => setFind(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  findFirst();
                }
              }}
              placeholder="Find a setting"
              aria-label="Find a setting"
              className="mb-2 h-7 text-xs"
            />
            {railSections.length === 0 && (
              <p className="px-2 py-1.5 text-xs text-[var(--text-tertiary)]">No setting matches.</p>
            )}
            {fieldHits.length > 0 && (
              <ul aria-label="Matching fields" className="mb-2 space-y-0.5 border-b border-[var(--border-subtle)] pb-2 text-[13px]">
                {fieldHits.map((h) => (
                  <li key={h.target}>
                    <button
                      type="button"
                      onClick={() => revealSettingsTarget(h.target)}
                      className="flex w-full flex-col rounded-md px-2 py-1 text-left text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)]/60"
                    >
                      <span className="truncate">{h.label}</span>
                      <span className="text-xs text-[var(--text-tertiary)]">in {SECTION_LABEL[h.section] ?? h.section}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <ul className="space-y-0.5 text-[13px]">
              {railSections.map((s) => {
                const isDirty = isSaveableSection(s.id) && dirty.includes(s.id);
                return (
                  <li key={s.id}>
                    <button
                      type="button"
                      onClick={() => jump(s.id)}
                      aria-current={active === s.id ? "true" : undefined}
                      className={cn(
                        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left transition-colors",
                        active === s.id
                          ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                          : "text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]/60 hover:text-[var(--text-primary)]",
                        s.id === "danger" && "text-[var(--error)] hover:text-[var(--error)]",
                      )}
                    >
                      <s.icon className="h-3.5 w-3.5 shrink-0" />
                      <span className="flex-1 truncate">{s.label}</span>
                      {isDirty && (
                        <DirtyDot immediate={IMMEDIATE.includes(s.id as SaveableSection)} />
                      )}
                    </button>
                  </li>
                );
              })}
            </ul>
          </nav>
        </div>

        <StagedChangesBar project={project} placement="sticky" />
        <DiffConfirmDialog
          open={pendingSave !== null}
          title={
            pendingSave && pendingSave.sections.length === 1
              ? `Apply ${SECTION_LABEL[pendingSave.sections[0]]} changes?`
              : "Apply service changes?"
          }
          description="Review what each change does to the running service before it applies."
          entries={diffEntries}
          confirmLabel="Apply changes"
          confirming={saving.length > 0}
          onCancel={() => setPendingSave(null)}
          onConfirm={() => {
            if (pendingSave) void applyPatch(pendingSave);
          }}
        />
      </div>
    </SettingsSaveProvider>
  );
}

// DirtyDot marks a nav entry: accent for staged edits, warning for an
// immediate card with unsaved input.
function DirtyDot({ immediate }: { immediate: boolean }) {
  const label = immediate ? "Unsaved changes" : "Staged changes";
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      className={cn(
        "h-1.5 w-1.5 shrink-0 rounded-full",
        immediate ? "bg-[var(--warning)]" : "bg-[var(--accent)]",
      )}
    />
  );
}

// useScrollSpy returns the id of the section whose top has passed the top
// of the overlay's scroll container (the one being read), not the next
// one coming up. `idsKey` is the comma-joined section list.
function useScrollSpy(rootRef: React.RefObject<HTMLDivElement | null>, idsKey: string): string {
  const ids = useMemo(() => idsKey.split(","), [idsKey]);
  const [active, setActive] = useState(ids[0] ?? "");
  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    let scroller: HTMLElement | null = root.parentElement;
    while (scroller && !/(auto|scroll)/.test(getComputedStyle(scroller).overflowY)) scroller = scroller.parentElement;
    const target: HTMLElement | Window = scroller ?? window;
    const update = () => {
      const top = scroller ? scroller.getBoundingClientRect().top : 0;
      let current = ids[0] ?? "";
      for (const id of ids) {
        const el = document.getElementById(id);
        if (!el) continue;
        // The section's scroll margin is the band a jump leaves above it
        // (room for the sticky strip on phones); past that, it's the one
        // being read. A few px of slack for subpixel layouts.
        const margin = parseFloat(getComputedStyle(el).scrollMarginTop) || 0;
        if (el.getBoundingClientRect().top - top <= margin + 8) current = id;
      }
      // At the very bottom the last short cards can't reach the top.
      if (scroller && scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 4) {
        current = ids[ids.length - 1] ?? current;
      }
      setActive(current);
    };
    update();
    target.addEventListener("scroll", update, { passive: true });
    return () => target.removeEventListener("scroll", update);
  }, [rootRef, ids]);
  return active;
}

// MobileSectionNav: below md the side nav becomes a sticky, horizontally
// scrolling strip. The active chip is kept in view.
function MobileSectionNav({
  sections,
  active,
  dirty,
  onJump,
}: {
  sections: readonly { id: string; label: string; icon: React.ComponentType<{ className?: string }> }[];
  active: string;
  dirty: SaveableSection[];
  onJump: (id: string) => void;
}) {
  const stripRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const strip = stripRef.current;
    const chip = strip?.querySelector<HTMLElement>(`[data-section="${active}"]`);
    if (!strip || !chip) return;
    const left = chip.offsetLeft - strip.clientWidth / 2 + chip.clientWidth / 2;
    strip.scrollTo({ left: Math.max(0, left), behavior: smoothScroll() });
  }, [active]);
  return (
    <nav
      aria-label="Settings sections"
      className="sticky top-0 z-10 border-b border-[var(--border-subtle)] bg-[var(--bg-primary)]/95 backdrop-blur md:hidden"
    >
      <div ref={stripRef} className="flex gap-1.5 overflow-x-auto px-3 py-2 [scrollbar-width:none]">
        {sections.map((s) => (
          <button
            key={s.id}
            type="button"
            data-section={s.id}
            onClick={() => onJump(s.id)}
            aria-current={active === s.id ? "true" : undefined}
            className={cn(
              "inline-flex h-8 shrink-0 items-center gap-1.5 rounded-full border px-3 text-xs transition-colors",
              active === s.id
                ? "border-[var(--text-tertiary)] bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                : "border-[var(--border-subtle)] bg-[var(--bg-secondary)] text-[var(--text-secondary)]",
              s.id === "danger" && "text-[var(--error)]",
            )}
          >
            <s.icon className="h-3.5 w-3.5" />
            {s.label}
            {isSaveableSection(s.id) && dirty.includes(s.id) && (
              <DirtyDot immediate={IMMEDIATE.includes(s.id)} />
            )}
          </button>
        ))}
      </div>
    </nav>
  );
}

// EnvBranchSection is the per-(env, service) branch override. Only
// rendered for non-production envs; production's branch is the Source
// card's. It saves on its own through PATCH .../env-groups/{env}/
// services/{service}/branch, and the build poller picks up new pushes to
// that branch as redeploys.
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
  const envs = useEnvironments(project);
  const fqn = `${project}-${service}`;
  const envRow = (envs.data ?? []).find(
    (e) => e.spec.service === fqn && (e.metadata.labels?.["kuso.sislelabs.com/env"] ?? "") === env,
  );
  // spec.branch is the OVERRIDE; empty means "fall back to the service
  // default branch".
  const overrideBranch = envRow?.spec.branch ?? "";
  const serviceDefaultBranch = svc?.spec?.repo?.defaultBranch ?? "main";
  const hasOverride = overrideBranch.trim() !== "";
  const repoLabel = (() => {
    // Credentials out FIRST: a gitlab deploy-token URL fell through the
    // github-only match below and rendered the token verbatim.
    const url = stripRepoCredentials(svc?.spec?.repo?.url ?? "");
    if (!url) return "";
    const m = url.match(/(?:github|gitlab)\.com[/:]([^/]+\/[^/.]+)/i);
    return m ? m[1] : url;
  })();
  // The field shows the branch actually deployed here: the override, or
  // the service default it falls back to.
  const effectiveBranch = hasOverride ? overrideBranch : serviceDefaultBranch;
  const [branch, setBranch] = useState(effectiveBranch);
  useEffect(() => {
    setBranch(effectiveBranch);
  }, [effectiveBranch]);
  // Empty stays unsavable: the server rejects an empty branch, so there
  // is no "clear override" call yet.
  const dirty = branch.trim() !== "" && branch.trim() !== effectiveBranch.trim();
  const [saving, setSaving] = useState(false);
  const qc = useQueryClient();
  const save = async () => {
    setSaving(true);
    try {
      await setEnvGroupServiceBranch(project, env, service, branch.trim());
      toast.success(`${service} in ${env} now tracks ${branch.trim()}. Push to that branch to redeploy.`);
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
      qc.invalidateQueries({ queryKey: ["projects", project, "env-groups"] });
      qc.invalidateQueries({ queryKey: ["projects", project], exact: true });
    } catch (e) {
      toastError(e, "Save failed");
    } finally {
      setSaving(false);
    }
  };
  return (
    <Section
      id="env-branch"
      scope={{ env }}
      applies="now"
      title={`Branch in ${env}`}
      icon={GitBranch}
      description={
        <>
          The branch {repoLabel ? <Code>{repoLabel}</Code> : "this service"} deploys in <Code>{env}</Code>. Production
          keeps its own branch from the Source card.
        </>
      }
    >
      <form
        className="flex flex-col gap-2 sm:flex-row"
        onSubmit={(e) => {
          e.preventDefault();
          if (dirty && !saving) void save();
        }}
      >
        <Input
          value={branch}
          onChange={(e) => setBranch(e.target.value)}
          placeholder={serviceDefaultBranch}
          aria-label={`Branch ${service} tracks in ${env}`}
          spellCheck={false}
          className={cn(monoInputClass, "flex-1")}
        />
        <Button
          type="submit"
          size="sm"
          className="h-8"
          disabled={!dirty || saving}
          disabledReason={!dirty && !saving ? "No changes to save" : undefined}
        >
          {saving ? "Saving…" : "Save branch"}
        </Button>
      </form>
      <p className="text-xs text-[var(--text-secondary)]">
        {hasOverride ? (
          <>
            Deploys <Code>{overrideBranch}</Code> here (override). The service default is{" "}
            <Code>{serviceDefaultBranch}</Code>.
          </>
        ) : (
          <>
            No override: <Code>{env}</Code> deploys the service default, <Code>{serviceDefaultBranch}</Code>.
          </>
        )}
      </p>
    </Section>
  );
}
