"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toast } from "sonner";
import { usePatchService } from "@/features/services";
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
} from "lucide-react";
import { cn } from "@/lib/utils";

import { useOverlayDirty } from "@/components/service/ServiceOverlay";
import { DiffConfirmDialog, type DiffEntry } from "@/components/shared/DiffConfirmDialog";
import { serviceBlast, type BlastInfo } from "@/lib/blast-radius";
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
  isSectionDirty,
  scopedState,
  type SaveableSection,
} from "./settings/sectionFields";
import { SourceSection } from "./settings/SourceSection";
import { DomainsCard } from "./settings/DomainsCard";
import { NetworkingSection } from "./settings/NetworkingSection";
import { ScaleSection } from "./settings/ScaleSection";
import { SleepSection } from "./settings/SleepSection";
import { PlacementSection } from "./settings/PlacementSection";
import { VolumesSection } from "./settings/VolumesSection";
import { BuildSection } from "./settings/BuildSection";
import { DeploySection } from "./settings/DeploySection";
import { ReleaseSection } from "./settings/ReleaseSection";
import { UptimeSection } from "./settings/UptimeSection";
import { HealthcheckSection } from "./settings/HealthcheckSection";
import type { ServicePatchBody } from "./settings/healthcheckPatch";
import { SecuritySection } from "./settings/SecuritySection";
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
  { id: "security", label: "Security", icon: ShieldAlert },
  { id: "danger", label: "Danger zone", icon: Trash2 },
] as const;

// Blast radius for fields lib/blast-radius.ts doesn't list yet.
const LOCAL_BLAST: Record<string, BlastInfo> = {
  healthcheck: {
    level: "warn",
    message:
      "Rewrites the probes → rolling restart of every environment. If the path doesn't answer, new pods never go ready and the old ones keep serving.",
  },
};

const SECTION_LABEL: Record<string, string> = Object.fromEntries(SECTIONS.map((s) => [s.id, s.label]));
const ALL_SAVEABLE = Object.keys(SECTION_FIELDS) as SaveableSection[];
const SAVED_FLASH_MS = 3000;

interface PendingSave {
  sections: SaveableSection[];
  target: FormState;
  body: ServicePatchBody;
}

// ServiceSettingsPanel orchestrates the per-service settings tab. Each
// card lives in ./settings/<Name>Section.tsx and saves only its own
// fields (sectionFields.ts); this file owns the form state, the save /
// confirm flow and the section nav.
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

  const baseline = useMemo(() => fromSvc(svc), [svc]);
  const [state, setState] = useState<FormState>(baseline);
  const [saving, setSaving] = useState<SaveableSection[]>([]);
  const [errors, setErrors] = useState<Partial<Record<SaveableSection, string>>>({});
  const [savedAt, setSavedAt] = useState<Partial<Record<SaveableSection, number>>>({});
  const [pendingSave, setPendingSave] = useState<PendingSave | null>(null);
  const patch = usePatchService(project, service);
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);

  // When fresh service data lands, re-baseline every card the user hasn't
  // edited, plus the cards that were just saved (adoptRef), so a server
  // that normalises a value doesn't leave a just-saved card "dirty".
  // Cards with in-flight edits keep them.
  const prevBaselineRef = useRef<FormState>(baseline);
  const adoptRef = useRef<Set<SaveableSection>>(new Set());
  useEffect(() => {
    const prevBase = prevBaselineRef.current;
    setState((prev) => {
      let next = prev;
      for (const s of ALL_SAVEABLE) {
        if (adoptRef.current.has(s) || !isSectionDirty(s, prev, prevBase)) next = discardSection(s, next, baseline);
      }
      return next;
    });
    adoptRef.current.clear();
    prevBaselineRef.current = baseline;
  }, [baseline]);

  const dirty = useMemo(() => dirtySections(state, baseline), [state, baseline]);

  // Clear the "Saved" flash after a few seconds.
  useEffect(() => {
    const ids = Object.keys(savedAt) as SaveableSection[];
    if (ids.length === 0) return;
    const t = window.setTimeout(() => setSavedAt({}), SAVED_FLASH_MS);
    return () => window.clearTimeout(t);
  }, [savedAt]);

  const requestSave = useCallback(
    (sections: SaveableSection[]) => {
      const target = scopedState(sections, state, baseline);
      const res = buildServicePatch(target, baseline, {
        command: svc?.spec.command,
        releaseCommand: svc?.spec.release?.command,
      });
      if ("error" in res) {
        // Attribute the error to the single card being saved; a save-all
        // error shows on every dirty card involved.
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
        // Only cosmetic edits (an empty row added then removed): reset.
        setState((s) => sections.reduce((acc, sec) => discardSection(sec, acc, baseline), s));
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
      for (const s of sections) adoptRef.current.add(s);
      // The repo token is write-only: it never comes back, so clear it.
      if (body.repo?.token) setState((s) => ({ ...s, repoToken: "" }));
      setSavedAt(Object.fromEntries(sections.map((s) => [s, Date.now()])));
      toast.success(sections.length === 1 ? `${SECTION_LABEL[sections[0]]} saved` : "Changes saved");
    } catch (e) {
      const msg = e instanceof Error ? e.message : "Failed to save";
      toast.error(msg);
      setErrors((prev) => ({ ...prev, ...Object.fromEntries(sections.map((s) => [s, msg])) }));
    } finally {
      setPendingSave(null);
      setSaving([]);
    }
  };

  const discardAll = () => {
    setState(baseline);
    setErrors({});
  };

  // The tab-switch / close guard in the overlay shell still needs to know
  // about unsaved edits. No onSave here: the cards and the bar below own
  // saving, so the shell's generic save bar stays hidden.
  useOverlayDirty("settings", dirty.length > 0 && canWrite, { onDiscard: discardAll });

  const saveApi = useMemo<SettingsSaveApi>(
    () => ({
      canWrite,
      status: (id) => {
        if (!isSaveableSection(id)) return null;
        return {
          dirty: dirty.includes(id),
          saving: saving.includes(id),
          saved: savedAt[id] !== undefined,
          error: errors[id] ?? null,
        };
      },
      save: (id) => {
        if (isSaveableSection(id)) requestSave([id]);
      },
      discard: (id) => {
        if (!isSaveableSection(id)) return;
        setState((s) => discardSection(id, s, baseline));
        setErrors((e) => {
          const next = { ...e };
          delete next[id];
          return next;
        });
      },
    }),
    [canWrite, dirty, saving, savedAt, errors, requestSave, baseline],
  );

  const diffEntries: DiffEntry[] = pendingSave
    ? Object.keys(pendingSave.body).map((field) => {
        const [before, after] = fieldDiffValues(field, baseline, pendingSave.target);
        return { field, before, after, warning: serviceBlast(field) ?? LOCAL_BLAST[field] };
      })
    : [];

  // Uptime checks ping the HTTP port, so workers and internal-only
  // services have nothing to check. Domains need a public ingress.
  const uptimeApplies = state.runtime !== "worker" && !state.internal;
  const domainsApply = !baseline.internal;
  // Workers have no port, so the chart renders no probes at all.
  const healthcheckApplies = state.runtime !== "worker";
  const sections = SECTIONS.filter(
    (s) =>
      (s.id !== "uptime" || uptimeApplies) &&
      (s.id !== "domains" || domainsApply) &&
      (s.id !== "healthcheck" || healthcheckApplies),
  );

  const rootRef = useRef<HTMLDivElement>(null);
  const active = useScrollSpy(rootRef, sections.map((s) => s.id).join(","));
  const jump = (id: string) => {
    document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
  };

  return (
    <SettingsSaveProvider value={saveApi}>
      <div ref={rootRef} className="relative">
        <MobileSectionNav sections={sections} active={active} dirty={dirty} onJump={jump} />
        <div className="grid grid-cols-1 gap-0 pb-28 md:grid-cols-[minmax(0,1fr)_168px]">
          <div className="min-w-0 space-y-6 px-3 py-4 sm:px-4 md:px-6 md:py-6">
            {!onProduction && env && <EnvBranchSection project={project} env={env} service={service} svc={svc} />}
            <SourceSection state={state} setState={setState} project={project} service={service} />
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
            <NetworkingSection state={state} setState={setState} />
            <ScaleSection state={state} setState={setState} />
            <SleepSection state={state} setState={setState} />
            <PlacementSection state={state} setState={setState} />
            <VolumesSection state={state} setState={setState} existingVolumes={baseline.volumes} />
            <BuildSection state={state} setState={setState} project={project} />
            <DeploySection project={project} state={state} setState={setState} />
            <ReleaseSection state={state} setState={setState} />
            {healthcheckApplies && <HealthcheckSection state={state} setState={setState} />}
            {uptimeApplies && <UptimeSection project={project} state={state} setState={setState} />}
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
            <ul className="space-y-0.5 text-[13px]">
              {sections.map((s) => {
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
                        <span
                          aria-label="Unsaved changes"
                          title="Unsaved changes"
                          className="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--warning)]"
                        />
                      )}
                    </button>
                  </li>
                );
              })}
            </ul>
          </nav>
        </div>

        {dirty.length > 0 && canWrite && (
          <div className="pointer-events-none sticky bottom-3 z-20 flex justify-center px-3">
            <div
              role="region"
              aria-label="Unsaved settings"
              className="pointer-events-auto flex max-w-full flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-[var(--border-default)] bg-[var(--bg-elevated)] px-3 py-2 shadow-[var(--shadow-lg)]"
            >
              <span className="inline-flex min-w-0 items-center gap-1.5 text-xs text-[var(--text-primary)]">
                <span aria-hidden className="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--warning)]" />
                <span className="truncate">
                  Unsaved changes in {dirty.map((d) => SECTION_LABEL[d]).join(", ")}
                </span>
              </span>
              <div className="flex items-center gap-1.5">
                <Button variant="ghost" size="sm" onClick={discardAll} disabled={saving.length > 0}>
                  Discard all
                </Button>
                <Button size="sm" onClick={() => requestSave(dirty)} disabled={saving.length > 0}>
                  {saving.length > 0 ? "Saving…" : dirty.length > 1 ? "Save all" : "Save"}
                </Button>
              </div>
            </div>
          </div>
        )}
        {dirty.length > 0 && !canWrite && (
          <div className="pointer-events-none sticky bottom-3 z-20 flex justify-center px-3">
            <span className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-3 py-2 text-xs text-[var(--text-secondary)] shadow-[var(--shadow-md)]">
              Read-only: your role can&apos;t edit services
            </span>
          </div>
        )}
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

// useScrollSpy returns the id of the section nearest the top of the
// overlay's scroll container. `idsKey` is the comma-joined section list.
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
        if (el && el.getBoundingClientRect().top - top <= 96) current = id;
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
    strip.scrollTo({ left: Math.max(0, left), behavior: "smooth" });
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
              <span aria-label="Unsaved changes" className="h-1.5 w-1.5 rounded-full bg-[var(--warning)]" />
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
  const [branch, setBranch] = useState(overrideBranch);
  useEffect(() => {
    setBranch(overrideBranch);
  }, [overrideBranch]);
  // Empty stays unsavable: the server rejects an empty branch, so there
  // is no "clear override" call yet.
  const dirty = branch.trim() !== "" && branch.trim() !== overrideBranch.trim();
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
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };
  return (
    <Section
      id="env-branch"
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
          placeholder={env}
          aria-label={`Branch ${service} tracks in ${env}`}
          spellCheck={false}
          className={cn(monoInputClass, "flex-1")}
        />
        <Button type="submit" size="sm" className="h-8" disabled={!dirty || saving}>
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
