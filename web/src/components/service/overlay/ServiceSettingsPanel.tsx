"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "sonner";
import { useDeleteProject, useProject } from "@/features/projects";
import { usePatchService, type PatchServiceBody } from "@/features/services";
import { useCan, Perms } from "@/features/auth";
import { useRouter } from "next/navigation";
import type { KusoService, KusoVolume } from "@/types/projects";
import {
  Github, Trash2, Network, Layers3, Hammer, Cloud, Save, HardDrive, Plus, X, ExternalLink, MapPin,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { cn } from "@/lib/utils";

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

const RUNTIMES = ["dockerfile", "nixpacks", "static", "buildpacks"] as const;

// FormState is the entire editable surface of the service spec.
// Hoisted up here so the floating save bar can see ALL dirty
// fields at once (vs. the old per-section state). Strings for
// numeric inputs so empty fields don't snap to 0 mid-typing.
interface FormState {
  // Source
  repoURL: string;
  repoBranch: string;
  repoPath: string;
  repoInstallationID: number; // 0 = unchanged / inherit
  // Networking
  port: string;
  domains: string; // newline-separated
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
}

interface PlacementRow {
  key: string;
  value: string;
}

interface VolumeRow {
  name: string;
  mountPath: string;
  sizeGi: number;
}

function fromSvc(svc?: KusoService): FormState {
  const repo = svc?.spec.repo;
  // Service-level installationId is on spec.github.installationId; the
  // type may not declare it yet (we ship that as a non-breaking
  // addition), so cast through unknown to read it without forcing a
  // type-system rev.
  const ghSpec = (svc?.spec as { github?: { installationId?: number } } | undefined)?.github;
  return {
    repoURL: repo?.url ?? "",
    repoBranch: repo?.defaultBranch ?? "",
    repoPath: repo?.path && repo.path !== "." ? repo.path : "",
    repoInstallationID: ghSpec?.installationId ?? 0,
    port: String(svc?.spec.port ?? 8080),
    domains: (svc?.spec.domains ?? []).map((d) => d.host ?? "").filter(Boolean).join("\n"),
    scaleMin: String(svc?.spec.scale?.min ?? 1),
    scaleMax: String(svc?.spec.scale?.max ?? 5),
    scaleCPU: String(svc?.spec.scale?.targetCPU ?? 70),
    runtime: svc?.spec.runtime ?? "dockerfile",
    volumes: (svc?.spec.volumes ?? []).map((v: KusoVolume) => ({
      name: v.name,
      mountPath: v.mountPath,
      sizeGi: v.sizeGi ?? 1,
    })),
    placement: Object.entries(svc?.spec.placement?.labels ?? {}).map(([k, v]) => ({ key: k, value: v })),
    placementNodes: svc?.spec.placement?.nodes ?? [],
  };
}

// shallowEqual compares two form states. We use it to drive the
// "dirty" flag for the floating save bar — JSON.stringify is fine
// for this shape (no Date / Map / Set) and 10× cheaper than a
// hand-rolled walk.
function isEqual(a: FormState, b: FormState): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

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
          <SourceSection state={state} setState={setState} />
          <NetworkingSection state={state} setState={setState} />
          <ScaleSection state={state} setState={setState} />
          <PlacementSection state={state} setState={setState} />
          <VolumesSection state={state} setState={setState} />
          <BuildSection state={state} setState={setState} />
          <DeploySection project={project} />
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
                    s.id === "danger" && "text-red-400/70 hover:text-red-400"
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
      <FloatingSaveBar
        dirty={dirty && canWrite}
        pending={pending}
        onSave={onSave}
        onReset={reset}
      />
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

// ---------- Floating save bar ----------

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

// ---------- Layout primitives ----------

function Section({
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
function Row({
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
        "flex items-center gap-3 px-3 py-2",
        !last && "border-b border-[var(--border-subtle)]"
      )}
    >
      <div className="min-w-[140px]">
        <div className="text-[12px] text-[var(--text-secondary)]">{label}</div>
        {hint && (
          <div className="font-mono text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>
        )}
      </div>
      <div className="ml-auto flex min-w-0 flex-1 items-center justify-end gap-2">{control}</div>
    </div>
  );
}

// ---------- Sections ----------

function SourceSection({ state, setState }: SectionProps) {
  // Pull installations so the user can re-point to a repo behind a
  // different GH App install. Best-effort: we don't gate the rest of
  // the section on this query landing.
  const installs = useQuery({
    queryKey: ["github", "installations"],
    queryFn: () =>
      api<{ id: number; accountLogin: string; repositories: { fullName: string }[] }[]>(
        "/api/github/installations"
      ),
    staleTime: 60_000,
  });
  const repoDisplay = state.repoURL.replace(/^https?:\/\/(www\.)?/, "");

  return (
    <Section id="source" title="Source" icon={Github}>
      <Row
        label="repository"
        hint="full https URL"
        control={
          <div className="flex w-full items-center gap-1.5">
            <Input
              value={state.repoURL}
              onChange={(e) => setState((s) => ({ ...s, repoURL: e.target.value }))}
              placeholder="https://github.com/owner/repo"
              className="h-7 flex-1 font-mono text-[12px]"
              spellCheck={false}
            />
            {state.repoURL && (
              <a
                href={state.repoURL}
                target="_blank"
                rel="noreferrer"
                aria-label="Open in new tab"
                title={repoDisplay}
                className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <ExternalLink className="h-3 w-3" />
              </a>
            )}
          </div>
        }
      />
      <Row
        label="branch"
        hint="default deploy branch"
        control={
          <Input
            value={state.repoBranch}
            onChange={(e) => setState((s) => ({ ...s, repoBranch: e.target.value }))}
            placeholder="main"
            className="h-7 w-40 font-mono text-[12px]"
            spellCheck={false}
          />
        }
      />
      <Row
        label="path"
        hint="monorepo subdir; leave blank for root"
        control={
          <Input
            value={state.repoPath}
            onChange={(e) => setState((s) => ({ ...s, repoPath: e.target.value }))}
            placeholder="apps/api"
            className="h-7 w-48 font-mono text-[12px]"
            spellCheck={false}
          />
        }
      />
      <Row
        label="installation"
        hint="GitHub App that owns the repo"
        control={
          <select
            value={state.repoInstallationID || 0}
            onChange={(e) =>
              setState((s) => ({ ...s, repoInstallationID: Number(e.target.value) || 0 }))
            }
            className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px]"
          >
            <option value={0}>(unchanged)</option>
            {(installs.data ?? []).map((inst) => (
              <option key={inst.id} value={inst.id}>
                {inst.accountLogin} ({inst.repositories.length} repos)
              </option>
            ))}
          </select>
        }
        last
      />
    </Section>
  );
}

interface SectionProps {
  state: FormState;
  setState: React.Dispatch<React.SetStateAction<FormState>>;
}

function NetworkingSection({ state, setState }: SectionProps) {
  return (
    <Section id="networking" title="Networking" icon={Network}>
      <Row
        label="port"
        hint="container port"
        control={
          <Input
            type="number"
            value={state.port}
            onChange={(e) => setState((s) => ({ ...s, port: e.target.value }))}
            min={1}
            max={65535}
            className="h-7 w-24 font-mono text-[12px]"
          />
        }
      />
      <Row
        label="domains"
        hint="auto-TLS; one per line"
        control={
          <textarea
            value={state.domains}
            onChange={(e) => setState((s) => ({ ...s, domains: e.target.value }))}
            spellCheck={false}
            placeholder="api.example.com"
            rows={Math.max(1, Math.min(4, state.domains.split("\n").length))}
            className="w-full max-w-[320px] resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1 font-mono text-[12px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
          />
        }
        last
      />
    </Section>
  );
}

function ScaleSection({ state, setState }: SectionProps) {
  const min = Number(state.scaleMin);
  const sleeps = min === 0;
  return (
    <Section
      id="scale"
      title="Scale"
      icon={Layers3}
      hint={sleeps ? "sleeps when idle" : `keeps ${min} pod${min === 1 ? "" : "s"} warm`}
    >
      <Row
        label="min replicas"
        hint="0 = sleep when idle"
        control={
          <Input
            type="number"
            value={state.scaleMin}
            onChange={(e) => setState((s) => ({ ...s, scaleMin: e.target.value }))}
            className="h-7 w-20 font-mono text-[12px]"
            min={0}
          />
        }
      />
      <Row
        label="max replicas"
        hint="autoscale ceiling"
        control={
          <Input
            type="number"
            value={state.scaleMax}
            onChange={(e) => setState((s) => ({ ...s, scaleMax: e.target.value }))}
            className="h-7 w-20 font-mono text-[12px]"
            min={1}
          />
        }
      />
      <Row
        label="cpu threshold"
        hint="add a replica past this %"
        control={
          <div className="inline-flex items-center gap-1.5">
            <Input
              type="number"
              value={state.scaleCPU}
              onChange={(e) => setState((s) => ({ ...s, scaleCPU: e.target.value }))}
              className="h-7 w-16 font-mono text-[12px]"
              min={1}
              max={100}
            />
            <span className="font-mono text-[11px] text-[var(--text-tertiary)]">%</span>
          </div>
        }
        last
      />
    </Section>
  );
}

function VolumesSection({ state, setState }: SectionProps) {
  const add = () =>
    setState((s) => ({
      ...s,
      volumes: [...s.volumes, { name: "", mountPath: "", sizeGi: 1 }],
    }));
  const update = (i: number, patch: Partial<VolumeRow>) =>
    setState((s) => ({
      ...s,
      volumes: s.volumes.map((v, j) => (j === i ? { ...v, ...patch } : v)),
    }));
  const remove = (i: number) =>
    setState((s) => ({ ...s, volumes: s.volumes.filter((_, j) => j !== i) }));

  return (
    <Section
      id="volumes"
      title="Volumes"
      icon={HardDrive}
      hint={state.volumes.length === 0 ? "none" : `${state.volumes.length}`}
    >
      {state.volumes.length === 0 ? (
        <p className="px-3 py-2.5 text-[11px] text-[var(--text-tertiary)]">
          No persistent volumes. Add one for SQLite, file uploads, or any state that should
          survive pod restarts.
        </p>
      ) : (
        state.volumes.map((v, i) => (
          <div
            key={i}
            className="grid grid-cols-[120px_1fr_72px_28px] items-center gap-1.5 border-b border-[var(--border-subtle)] px-3 py-1.5 last:border-b-0"
          >
            <Input
              value={v.name}
              onChange={(e) => update(i, { name: e.target.value })}
              placeholder="data"
              className="h-7 font-mono text-[11px]"
            />
            <Input
              value={v.mountPath}
              onChange={(e) => update(i, { mountPath: e.target.value })}
              placeholder="/var/lib/app"
              className="h-7 font-mono text-[11px]"
            />
            <div className="relative">
              <Input
                type="number"
                value={v.sizeGi}
                onChange={(e) =>
                  update(i, { sizeGi: Math.max(1, Number(e.target.value) || 1) })
                }
                min={1}
                className="h-7 pr-6 font-mono text-[11px]"
              />
              <span className="pointer-events-none absolute right-1.5 top-1/2 -translate-y-1/2 font-mono text-[10px] text-[var(--text-tertiary)]">
                Gi
              </span>
            </div>
            <button
              type="button"
              onClick={() => remove(i)}
              aria-label="Remove volume"
              className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
            >
              <X className="h-3 w-3" />
            </button>
          </div>
        ))
      )}
      <button
        type="button"
        onClick={add}
        className="flex w-full items-center gap-1.5 border-t border-[var(--border-subtle)] px-3 py-2 text-left text-[11px] text-[var(--accent)] hover:bg-[var(--bg-tertiary)]/40"
      >
        <Plus className="h-3 w-3" />
        add volume
      </button>
    </Section>
  );
}

interface NodeSummary {
  name: string;
  ready: boolean;
  region?: string;
  kusoLabels?: Record<string, string>;
}

function PlacementSection({ state, setState }: SectionProps) {
  // Pull live nodes so the user can pick from real labels + hostnames
  // instead of guessing. We don't gate the section on the response —
  // empty cluster means a friendlier "no nodes labeled yet" hint.
  const nodes = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
    staleTime: 30_000,
  });
  const allLabels = new Map<string, Set<string>>();
  for (const n of nodes.data ?? []) {
    for (const [k, v] of Object.entries(n.kusoLabels ?? {})) {
      if (!allLabels.has(k)) allLabels.set(k, new Set());
      allLabels.get(k)!.add(v);
    }
  }
  const allHostnames = (nodes.data ?? []).map((n) => n.name);

  // Live preview: which nodes match the current label+nodes selection?
  const matching = (nodes.data ?? []).filter((n) => {
    for (const r of state.placement) {
      if (!r.key.trim()) continue;
      if ((n.kusoLabels ?? {})[r.key.trim()] !== r.value) return false;
    }
    if (state.placementNodes.length > 0 && !state.placementNodes.includes(n.name)) return false;
    return true;
  });

  const addLabel = () =>
    setState((s) => ({ ...s, placement: [...s.placement, { key: "", value: "" }] }));
  const updLabel = (i: number, patch: Partial<PlacementRow>) =>
    setState((s) => ({
      ...s,
      placement: s.placement.map((r, j) => (j === i ? { ...r, ...patch } : r)),
    }));
  const rmLabel = (i: number) =>
    setState((s) => ({ ...s, placement: s.placement.filter((_, j) => j !== i) }));

  const toggleNode = (name: string) =>
    setState((s) => ({
      ...s,
      placementNodes: s.placementNodes.includes(name)
        ? s.placementNodes.filter((n) => n !== name)
        : [...s.placementNodes, name],
    }));

  const totalNodes = (nodes.data ?? []).length;

  // Treat unfilled rule rows as "no rule yet" rather than letting them
  // skew the live match count. Without this, opening "+ add label rule"
  // and leaving the row blank reads "1/1 match" because the empty key
  // was being skipped — so the user couldn't tell if the rule had taken
  // effect or not. Trivially-true rows now show as "incomplete".
  const incompleteRules = state.placement.filter((r) => !r.key.trim() || !r.value.trim()).length;
  const hasEffectiveRules =
    state.placement.some((r) => r.key.trim() && r.value.trim()) || state.placementNodes.length > 0;

  return (
    <Section
      id="placement"
      title="Placement"
      icon={MapPin}
      hint={
        !hasEffectiveRules
          ? "schedules anywhere"
          : incompleteRules > 0
            ? `${incompleteRules} incomplete`
            : `${matching.length}/${totalNodes} match`
      }
    >
      <p className="border-b border-[var(--border-subtle)] px-3 py-2 text-[11px] text-[var(--text-secondary)]">
        Pin this service to a subset of cluster nodes. Use{" "}
        <span className="font-mono text-[var(--text-tertiary)]">key=value</span> rules
        (e.g. <span className="font-mono">region=eu</span> or{" "}
        <span className="font-mono">tier=gpu</span>) to match nodes by label, or pick
        specific hostnames below. Nodes need both — empty rules schedule anywhere.
      </p>
      {/* Label rules */}
      {state.placement.length === 0 ? (
        <p className="px-3 py-2.5 text-[11px] text-[var(--text-tertiary)]">
          No label rules.
        </p>
      ) : (
        state.placement.map((r, i) => {
          const valuesForKey = allLabels.get(r.key.trim());
          return (
            <div
              key={i}
              className="grid grid-cols-[140px_1fr_28px] items-center gap-1.5 border-b border-[var(--border-subtle)] px-3 py-1.5 last:border-b-0"
            >
              <Input
                value={r.key}
                onChange={(e) => updLabel(i, { key: e.target.value })}
                placeholder="region"
                list="kuso-placement-keys"
                className="h-7 font-mono text-[11px]"
              />
              <Input
                value={r.value}
                onChange={(e) => updLabel(i, { value: e.target.value })}
                placeholder="eu"
                list={valuesForKey ? `kuso-placement-values-${i}` : undefined}
                className="h-7 font-mono text-[11px]"
              />
              {valuesForKey && (
                <datalist id={`kuso-placement-values-${i}`}>
                  {[...valuesForKey].map((v) => (
                    <option key={v} value={v} />
                  ))}
                </datalist>
              )}
              <button
                type="button"
                onClick={() => rmLabel(i)}
                aria-label="Remove rule"
                className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
              >
                <X className="h-3 w-3" />
              </button>
            </div>
          );
        })
      )}
      <button
        type="button"
        onClick={addLabel}
        className="flex w-full items-center gap-1.5 border-y border-[var(--border-subtle)] px-3 py-2 text-left text-[11px] text-[var(--accent)] hover:bg-[var(--bg-tertiary)]/40"
      >
        <Plus className="h-3 w-3" />
        add label rule
      </button>

      {/* Datalist for label keys is shared across rows. */}
      <datalist id="kuso-placement-keys">
        {[...allLabels.keys()].map((k) => (
          <option key={k} value={k} />
        ))}
      </datalist>

      {/* Specific-node pinning */}
      <div className="px-3 py-2">
        <div className="mb-1.5 flex items-center justify-between">
          <span className="text-[12px] text-[var(--text-secondary)]">specific nodes</span>
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            {state.placementNodes.length === 0
              ? "any node matching the labels"
              : `${state.placementNodes.length} pinned`}
          </span>
        </div>
        {allHostnames.length === 0 ? (
          <p className="text-[11px] text-[var(--text-tertiary)]">No nodes visible yet.</p>
        ) : (
          <div className="flex flex-wrap gap-1">
            {allHostnames.map((h) => {
              const picked = state.placementNodes.includes(h);
              return (
                <button
                  key={h}
                  type="button"
                  onClick={() => toggleNode(h)}
                  className={cn(
                    "inline-flex h-6 items-center rounded-md border px-2 font-mono text-[10px] transition-colors",
                    picked
                      ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                      : "border-[var(--border-subtle)] bg-[var(--bg-primary)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
                  )}
                >
                  {h}
                </button>
              );
            })}
          </div>
        )}
      </div>

      {/* Live match preview — only meaningful once there's at least
          one fully-filled rule or a pinned hostname. Showing it for an
          unfinished blank row was the source of the "1/1 match"
          confusion in the screenshots. */}
      {hasEffectiveRules && (
        <div className="border-t border-[var(--border-subtle)] px-3 py-2 text-[10px] text-[var(--text-tertiary)]">
          {incompleteRules > 0 ? (
            <span className="text-[var(--text-tertiary)]">
              fill in the {incompleteRules === 1 ? "empty rule" : "empty rules"} or remove {incompleteRules === 1 ? "it" : "them"} to see what matches
            </span>
          ) : matching.length === 0 ? (
            <span className="text-amber-400">
              ⚠ No nodes match. Pods would stay Pending.
            </span>
          ) : (
            <span>
              would schedule on:{" "}
              <span className="font-mono text-[var(--text-secondary)]">
                {matching.map((m) => m.name).join(", ")}
              </span>
            </span>
          )}
        </div>
      )}
    </Section>
  );
}

function BuildSection({ state, setState }: SectionProps) {
  return (
    <Section id="build" title="Build" icon={Hammer}>
      <Row
        label="strategy"
        hint="how kuso builds the image"
        control={
          <div className="inline-flex flex-wrap gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
            {RUNTIMES.map((r) => (
              <button
                key={r}
                type="button"
                onClick={() => setState((s) => ({ ...s, runtime: r }))}
                className={cn(
                  "rounded px-2 py-1 font-mono text-[11px] transition-colors",
                  state.runtime === r
                    ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                    : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
                )}
              >
                {r}
              </button>
            ))}
          </div>
        }
        last
      />
    </Section>
  );
}

function DeploySection({ project }: { project: string }) {
  // Read the project's previews config so the user sees, in this
  // service's settings, whether PR previews are on for the whole
  // project + how long they live. Saves them digging through the
  // project settings tab to answer "will my PR get its own URL?"
  const proj = useProject(project);
  type ProjSpec = { previews?: { enabled?: boolean; ttlDays?: number } };
  const spec = (proj.data as { project?: { spec?: ProjSpec } } | undefined)?.project?.spec;
  const previewsOn = !!spec?.previews?.enabled;
  const ttlDays = spec?.previews?.ttlDays ?? 7;

  return (
    <Section id="deploy" title="Deploy" icon={Cloud}>
      <div className="space-y-1 px-3 py-2.5 text-[12px] text-[var(--text-secondary)]">
        <p>
          Successful builds of <span className="font-mono">main</span> ship to{" "}
          <span className="font-mono">production</span>.
        </p>
        {previewsOn ? (
          <p>
            PR previews <span className="text-emerald-400">on</span> — every PR gets a
            throwaway env at <span className="font-mono">&lt;service&gt;-pr-N.&lt;project-domain&gt;</span>,
            auto-deleted after the PR closes or {ttlDays} days idle. Previews boot
            with no env vars (set per-env if needed).
          </p>
        ) : (
          <p>
            PR previews <span className="text-[var(--text-tertiary)]">off</span> for this
            project. Enable in{" "}
            <a
              href={`/projects/${encodeURIComponent(project)}/settings`}
              className="text-[var(--accent)] hover:underline"
            >
              project settings
            </a>
            {" "}to give each PR its own URL.
          </p>
        )}
      </div>
      <p className="border-t border-[var(--border-subtle)] px-3 py-2 font-mono text-[10px] text-[var(--text-tertiary)]">
        per-service preview opt-out coming next; today the project toggle covers all services
      </p>
    </Section>
  );
}

function DangerSection({ project, service }: { project: string; service: string }) {
  const router = useRouter();
  const del = useDeleteProject();
  const [confirming, setConfirming] = useState(false);
  const [confirmText, setConfirmText] = useState("");

  const onDelete = async () => {
    if (confirmText !== service) {
      toast.error("Type the service name to confirm");
      return;
    }
    try {
      await del.mutateAsync(project);
      toast.success("Project deleted");
      router.replace("/projects");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to delete");
    }
  };

  return (
    <section id="danger" className="scroll-mt-6">
      <header className="mb-2 flex items-center gap-2">
        <Trash2 className="h-3.5 w-3.5 text-red-400" />
        <h3 className="font-heading text-sm font-semibold tracking-tight text-red-400">
          Danger
        </h3>
      </header>
      <div className="rounded-md border border-red-500/30 bg-red-500/5 p-4">
        <h4 className="text-sm font-semibold">Delete project</h4>
        <p className="mt-1 text-xs text-[var(--text-secondary)]">
          Removes the project, every service, every preview env, and tears down the running
          pods. The git repo is untouched. This cannot be undone.
        </p>
        {!confirming ? (
          <Button variant="outline" size="sm" className="mt-3" onClick={() => setConfirming(true)}>
            <Trash2 className="h-3.5 w-3.5" /> Delete project
          </Button>
        ) : (
          <div className="mt-3 space-y-2">
            <Label htmlFor="confirm-del" className="text-xs">
              Type <span className="font-mono">{service}</span> to confirm
            </Label>
            <Input
              id="confirm-del"
              value={confirmText}
              onChange={(e) => setConfirmText(e.target.value)}
              className="font-mono text-sm"
              autoFocus
            />
            <div className="flex items-center gap-2">
              <Button
                variant="destructive"
                size="sm"
                onClick={onDelete}
                disabled={confirmText !== service || del.isPending}
              >
                {del.isPending ? "Deleting…" : "Confirm delete"}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setConfirming(false);
                  setConfirmText("");
                }}
              >
                Cancel
              </Button>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}
