"use client";

import { createContext, useContext, useId } from "react";
import type React from "react";
import { Check, CircleAlert } from "lucide-react";
import type { KusoService, KusoVolume } from "@/types/projects";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";

export { Switch } from "@/components/ui/switch";
export { Segmented } from "@/components/ui/segmented";
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
  // Not editable in the form, but the server replaces the volume list
  // wholesale on PATCH and both are immutable on a bound PVC: dropping
  // them turns RWX into RWO and wedges the helm upgrade.
  storageClass?: string;
  accessMode?: string;
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
  // repoProvider is the git-host override. "" = auto (server infers from
  // the URL host: gitlab.* → gitlab, else github). "github" | "gitlab"
  // force it. Kept as a free string so we never widen the union in the
  // form state; the write path narrows it before sending.
  repoProvider: string;
  // repoToken is the GitLab clone credential for a private GitLab repo.
  // WRITE-ONLY: the server never returns it, so this is ALWAYS "" in the
  // baseline (see fromSvc) and is never pre-filled from a read. A blank
  // field on save leaves any existing stored token untouched — the write
  // path only sends it when the user actually typed a value.
  repoToken: string;
  // watchPaths: push-build globs, one per line. "" = server default.
  watchPaths: string;
  // Networking. Custom domains aren't form state: they apply
  // immediately through the env domain endpoints (DomainsCard).
  port: string;
  internal: boolean;
  // Ingress limits. "" = inherit (project, then platform default).
  limitMaxConcurrent: string;
  limitRate: string;
  limitBurst: string;
  // Scale
  scaleMin: string;
  scaleMax: string;
  scaleCPU: string;
  // Autoscale speed overrides. "" = chart default.
  scaleUpWindow: string;
  scaleUpPods: string;
  scaleUpPercent: string;
  scaleDownWindow: string;
  // Sleep wake-on exclude paths (newline-separated). When the service
  // sleeps (min=0), any request to a listed path keeps the WHOLE
  // deployment warm — so a Stripe/GitHub webhook on a sleeping service
  // doesn't cold-start-503 the callback. Empty = no exclusions.
  sleepExcludePaths: string;
  // Sleep: sleepEnabled is spec.sleep.enabled (production env only);
  // sleepAfter is afterMinutes as a string; sleepNonProduction mirrors
  // spec.sleep.nonProduction ("" = default on).
  sleepEnabled: boolean;
  sleepAfter: string;
  sleepNonProduction: string;
  // Resources (pod CPU/memory requests+limits). Empty string = unset
  // (chart default). e.g. cpuRequest "100m", memRequest "128Mi".
  cpuRequest: string;
  cpuLimit: string;
  memRequest: string;
  memLimit: string;
  // Build
  runtime: string;
  // dockerfile overrides the Dockerfile filename for runtime=dockerfile
  // (relative to repo path). Empty = "Dockerfile".
  dockerfile: string;
  // Image reference for runtime=image services (registry path + tag).
  // Editing + saving these is the redeploy path for image services —
  // they bypass the build pipeline entirely.
  imageRepository: string;
  imageTag: string;
  // imagePullSecret: registry-credential Secret name ("" = anonymous).
  imagePullSecret: string;
  // Storage
  volumes: VolumeRow[];
  // Placement
  placement: PlacementRow[];
  placementNodes: string[];
  // Deploy
  previewsDisabled: boolean;
  waitForCI: boolean;
  // Uptime check: uptimeEnabled is the inverse of spec.uptime.disabled;
  // uptimePath "" = health check path, or "/".
  uptimeEnabled: boolean;
  uptimePath: string;
  // Security (advanced) — opt-in escape hatch for images that self-drop
  // root at runtime (setpriv/gosu/su-exec). capAdd is comma-separated
  // capability names without the CAP_ prefix, e.g. "SETUID, SETGID".
  // Empty capAdd + allowPrivilegeEscalation=false = kuso's hardened
  // default (drop ALL, no escalation).
  capAdd: string;
  allowPrivilegeEscalation: boolean;
  // HTTP health check (spec.healthcheck). Off = TCP probes only. The
  // number fields are strings so blank can mean "server default".
  hcEnabled: boolean;
  hcPath: string;
  hcPort: string;
  hcDelay: string;
  hcPeriod: string;
  hcTimeout: string;
  hcThreshold: string;
  // Release hook — a one-off Job that runs before each deploy is
  // promoted (migrations etc). releaseCommand is the argv joined by
  // spaces for editing as plain text; empty = no hook. releaseTimeout
  // is the cap in seconds as a string; "" = server default (900).
  releaseCommand: string;
  releaseTimeout: string;
  // Egress gates (spec.privateEgress / spec.platformApiEgress).
  privateEgress: boolean;
  platformApiEgress: boolean;
  snapshotBeforeDeploy: boolean;
  // Start command: the stored argv joined by spaces; "" = image default.
  startCommand: string;
  // Build-time config. buildArgs is KEY=VALUE per line; publicEnv is
  // one env-var name per line.
  buildArgs: string;
  publicEnv: string;
  staticBuilderImage: string;
  staticRuntimeImage: string;
  staticBuildCmd: string;
  staticOutputDir: string;
  buildpacksBuilderImage: string;
  buildpacksLifecycleImage: string;
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

function numStr(n: number | undefined): string {
  return n ? String(n) : "";
}

export function fromSvc(svc?: KusoService): FormState {
  const repo = svc?.spec.repo;
  // Service-level installationId is on spec.github.installationId; the
  // type may not declare it yet (we ship that as a non-breaking
  // addition), so cast through unknown to read it without forcing a
  // type-system rev.
  const ghSpec = svc?.spec?.github;
  // provider may be surfaced on spec.repo.provider once the server
  // detects/stores it. It's non-secret, so we DO seed it into the
  // baseline (unlike the token) — an explicit value here means the
  // provider row shows the stored driver and isn't marked dirty on load.
  const repoProvider = (repo as { provider?: string } | undefined)?.provider ?? "";
  return {
    displayName: svc?.spec.displayName ?? "",
    repoURL: repo?.url ?? "",
    repoBranch: repo?.defaultBranch ?? "",
    repoPath: repo?.path && repo.path !== "." ? repo.path : "",
    repoInstallationID: ghSpec?.installationId ?? 0,
    repoProvider,
    // Write-only — never returned by the server, so always blank on load.
    repoToken: "",
    watchPaths: (svc?.spec.watchPaths ?? []).join("\n"),
    port: String(svc?.spec.port ?? 8080),
    internal: !!svc?.spec?.internal,
    limitMaxConcurrent: String(svc?.spec?.requestLimits?.maxConcurrent || ""),
    limitRate: String(svc?.spec?.requestLimits?.ratePerSecond || ""),
    limitBurst: String(svc?.spec?.requestLimits?.burst || ""),
    scaleMin: String(svc?.spec.scale?.min ?? 1),
    scaleMax: String(svc?.spec.scale?.max ?? 5),
    scaleCPU: String(svc?.spec.scale?.targetCPU ?? 70),
    scaleUpWindow: String(svc?.spec.scale?.scaleUpStabilizationSeconds ?? ""),
    scaleUpPods: String(svc?.spec.scale?.scaleUpPods || ""),
    scaleUpPercent: String(svc?.spec.scale?.scaleUpPercent || ""),
    scaleDownWindow: String(svc?.spec.scale?.scaleDownStabilizationSeconds ?? ""),
    sleepExcludePaths: (svc?.spec?.sleep?.wakeOn?.excludePaths ?? []).join("\n"),
    sleepEnabled: !!svc?.spec.sleep?.enabled,
    sleepAfter: String(svc?.spec.sleep?.afterMinutes || 30),
    sleepNonProduction: svc?.spec.sleep?.nonProduction ?? "",
    cpuRequest: svc?.spec.resources?.requests?.cpu ?? "",
    cpuLimit: svc?.spec.resources?.limits?.cpu ?? "",
    memRequest: svc?.spec.resources?.requests?.memory ?? "",
    memLimit: svc?.spec.resources?.limits?.memory ?? "",
    runtime: svc?.spec.runtime ?? "dockerfile",
    dockerfile: svc?.spec.dockerfile ?? "",
    imageRepository: svc?.spec.image?.repository ?? "",
    imageTag: svc?.spec.image?.tag ?? "",
    imagePullSecret: svc?.spec.image?.pullSecret ?? "",
    volumes: (svc?.spec.volumes ?? []).map((v: KusoVolume) => ({
      name: v.name,
      mountPath: v.mountPath,
      sizeGi: v.sizeGi ?? 1,
      ...(v.storageClass ? { storageClass: v.storageClass } : {}),
      ...(v.accessMode ? { accessMode: v.accessMode } : {}),
    })),
    placement: Object.entries(svc?.spec.placement?.labels ?? {}).map(([k, v]) => ({
      key: k,
      value: v,
    })),
    placementNodes: svc?.spec.placement?.nodes ?? [],
    // Per-service preview opt-out lives on spec.previews.disabled. The
    // KusoService type may not declare it (newer field), so cast.
    previewsDisabled:
      !!svc?.spec?.previews?.disabled,
    waitForCI: !!svc?.spec.waitForCI,
    uptimeEnabled: svc?.spec.uptime?.disabled !== true,
    uptimePath: svc?.spec.uptime?.path ?? "",
    capAdd: (svc?.spec.securityContext?.capabilities?.add ?? []).join(", "),
    allowPrivilegeEscalation: !!svc?.spec.securityContext?.allowPrivilegeEscalation,
    hcEnabled: !!svc?.spec.healthcheck?.path,
    hcPath: svc?.spec.healthcheck?.path ?? "",
    hcPort: numStr(svc?.spec.healthcheck?.port),
    hcDelay: numStr(svc?.spec.healthcheck?.initialDelaySeconds),
    hcPeriod: numStr(svc?.spec.healthcheck?.periodSeconds),
    hcTimeout: numStr(svc?.spec.healthcheck?.timeoutSeconds),
    hcThreshold: numStr(svc?.spec.healthcheck?.failureThreshold),
    releaseCommand: (svc?.spec.release?.command ?? []).join(" "),
    releaseTimeout: svc?.spec.release?.timeoutSeconds ? String(svc.spec.release.timeoutSeconds) : "",
    privateEgress: !!svc?.spec.privateEgress,
    platformApiEgress: !!svc?.spec.platformApiEgress,
    snapshotBeforeDeploy: !!svc?.spec.snapshotBeforeDeploy,
    startCommand: (svc?.spec.command ?? []).join(" "),
    buildArgs: Object.keys(svc?.spec.buildArgs ?? {})
      .sort()
      .map((k) => `${k}=${svc?.spec.buildArgs?.[k] ?? ""}`)
      .join("\n"),
    publicEnv: (svc?.spec.publicEnv ?? []).join("\n"),
    staticBuilderImage: svc?.spec.static?.builderImage ?? "",
    staticRuntimeImage: svc?.spec.static?.runtimeImage ?? "",
    staticBuildCmd: svc?.spec.static?.buildCmd ?? "",
    staticOutputDir: svc?.spec.static?.outputDir ?? "",
    buildpacksBuilderImage: svc?.spec.buildpacks?.builderImage ?? "",
    buildpacksLifecycleImage: svc?.spec.buildpacks?.lifecycleImage ?? "",
  };
}

// isEqual compares two form states. JSON.stringify is fine for this
// shape (no Date / Map / Set).
export function isEqual(a: FormState, b: FormState): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}


// ----- layout primitives shared by every section -----
//
// Vercel-style cards: title + description, then each field as label and
// hint ABOVE a full-width control, then a footer with the card's save
// state. Hints are sans at 12px; mono is reserved for literal values.

// Shared control classes so textareas match <Input>; selects use ui/select.
export const textareaClass =
  "block w-full min-w-0 rounded-sm border border-border bg-input px-3 py-2 font-mono text-[12px] leading-5 text-[var(--text-primary)] outline-none transition-colors placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50";
export const inputClass = "h-8 text-[13px] md:text-[13px]";
export const monoInputClass = "h-8 font-mono text-[12px] md:text-[12px]";

export interface SectionSaveStatus {
  dirty: boolean;
  saving: boolean;
  // Set for a few seconds after a successful save of this card.
  saved: boolean;
  error: string | null;
}

export interface SettingsSaveApi {
  canWrite: boolean;
  status: (section: string) => SectionSaveStatus | null;
  save: (section: string) => void;
  discard: (section: string) => void;
}

const SettingsSaveContext = createContext<SettingsSaveApi | null>(null);
export const SettingsSaveProvider = SettingsSaveContext.Provider;

export function useCanWriteSettings(): boolean {
  return useContext(SettingsSaveContext)?.canWrite ?? false;
}

export function Section({
  id,
  title,
  icon: Icon,
  description,
  status,
  footerNote,
  children,
  tone = "default",
  scope = "all",
}: {
  id: string;
  title: string;
  // Which environments a save touches: "all" for service-spec fields,
  // an env name for per-environment cards, "none" to say nothing.
  scope?: "all" | "none" | { env: string };
  icon: React.ComponentType<{ className?: string }>;
  description?: React.ReactNode;
  // Short summary pill in the header ("autoscales 1 → 3").
  status?: React.ReactNode;
  // Shown in the footer while the card is clean, e.g. what a save does.
  footerNote?: React.ReactNode;
  children: React.ReactNode;
  tone?: "default" | "danger";
}) {
  const headingId = useId();
  return (
    <section
      id={id}
      aria-labelledby={headingId}
      className={cn(
        "scroll-mt-16 rounded-lg border bg-[var(--bg-secondary)]",
        tone === "danger" ? "border-[var(--error)]/35" : "border-[var(--border-subtle)]",
      )}
    >
      <header className="flex items-start gap-3 px-4 pt-4 sm:px-5 sm:pt-5">
        <Icon
          className={cn(
            "mt-0.5 h-4 w-4 shrink-0",
            tone === "danger" ? "text-[var(--error)]" : "text-[var(--text-tertiary)]",
          )}
        />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <h3
              id={headingId}
              className={cn(
                "font-heading text-[15px] font-semibold tracking-tight",
                tone === "danger" && "text-[var(--error)]",
              )}
            >
              {title}
            </h3>
            {scope !== "none" && (
              <span className="text-xs text-[var(--text-tertiary)]">
                {scope === "all" ? "Applies to all environments" : `Only the ${scope.env} environment`}
              </span>
            )}
            {status && (
              <span className="rounded-full border border-[var(--border-subtle)] px-2 py-0.5 text-xs text-[var(--text-secondary)]">
                {status}
              </span>
            )}
          </div>
          {description && (
            <p className="mt-1 text-[13px] leading-relaxed text-[var(--text-secondary)]">{description}</p>
          )}
        </div>
      </header>
      <div className="space-y-5 px-4 py-4 sm:px-5 sm:py-5">{children}</div>
      <SectionFooter section={id} note={footerNote} />
    </section>
  );
}

function SectionFooter({ section, note }: { section: string; note?: React.ReactNode }) {
  const ctx = useContext(SettingsSaveContext);
  const st = ctx?.status(section);
  if (!ctx || !st) return null;
  let left: React.ReactNode = note ? <span>{note}</span> : null;
  if (st.error) {
    left = (
      <span role="alert" className="inline-flex items-start gap-1.5 text-[var(--error)]">
        <CircleAlert className="mt-px h-3.5 w-3.5 shrink-0" />
        <span className="break-words">{st.error}</span>
      </span>
    );
  } else if (st.dirty) {
    left = (
      <span className="inline-flex items-center gap-1.5 text-[var(--text-primary)]">
        <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-[var(--warning)]" />
        Unsaved changes
      </span>
    );
  } else if (st.saved) {
    left = (
      <span className="inline-flex items-center gap-1.5 text-[var(--success)]">
        <Check className="h-3.5 w-3.5" />
        Saved
      </span>
    );
  }
  return (
    <footer className="flex min-h-12 flex-wrap items-center gap-x-3 gap-y-2 rounded-b-lg border-t border-[var(--border-subtle)] bg-[var(--bg-primary)]/40 px-4 py-2.5 sm:px-5">
      <div aria-live="polite" className="min-w-0 flex-1 text-xs leading-relaxed text-[var(--text-secondary)]">
        {left}
      </div>
      {ctx.canWrite ? (
        <div className="flex shrink-0 items-center gap-2">
          {st.dirty && (
            <Button variant="ghost" size="sm" onClick={() => ctx.discard(section)} disabled={st.saving}>
              Discard
            </Button>
          )}
          <Button
            size="sm"
            variant={st.dirty ? "default" : "outline"}
            onClick={() => ctx.save(section)}
            disabled={!st.dirty || st.saving}
            disabledReason={!st.dirty && !st.saving ? "No changes to save in this section" : undefined}
          >
            {st.saving ? "Saving…" : "Save"}
          </Button>
        </div>
      ) : (
        <span className="text-xs text-[var(--text-tertiary)]">Read-only for your role</span>
      )}
    </footer>
  );
}

// Field: label + hint above a full-width control.
export function Field({
  label,
  hint,
  htmlFor,
  children,
  className,
}: {
  label: React.ReactNode;
  hint?: React.ReactNode;
  // When set, the label is a real <label for>; otherwise the control
  // should carry its own aria-label.
  htmlFor?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("min-w-0 space-y-2", className)}>
      <div className="space-y-0.5">
        {htmlFor ? (
          <label htmlFor={htmlFor} className="block text-[13px] font-medium text-[var(--text-primary)]">
            {label}
          </label>
        ) : (
          <div className="text-[13px] font-medium text-[var(--text-primary)]">{label}</div>
        )}
        {hint && <p className="text-xs leading-relaxed text-[var(--text-secondary)]">{hint}</p>}
      </div>
      {children}
    </div>
  );
}

// FieldGrid puts short related fields side by side on wider screens
// (min/max replicas, request/limit) and stacks them on phones.
export function FieldGrid({ children, cols = 2 }: { children: React.ReactNode; cols?: 2 | 3 }) {
  return (
    <div className={cn("grid grid-cols-1 gap-4", cols === 2 ? "sm:grid-cols-2" : "sm:grid-cols-3")}>
      {children}
    </div>
  );
}

// SwitchField: a boolean setting. Label + hint on the left, the switch on
// the right; the hint still gets the full remaining width.
export function SwitchField({
  label,
  hint,
  on,
  onChange,
  disabled,
  stateLabel,
}: {
  label: string;
  hint?: React.ReactNode;
  on: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  // Optional word next to the switch ("Sleeps when idle" / "Always on").
  stateLabel?: string;
}) {
  const labelId = useId();
  const hintId = useId();
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0 space-y-0.5">
        <div id={labelId} className="text-[13px] font-medium text-[var(--text-primary)]">
          {label}
        </div>
        {hint && (
          <p id={hintId} className="text-xs leading-relaxed text-[var(--text-secondary)]">
            {hint}
          </p>
        )}
      </div>
      <div className="flex shrink-0 items-center gap-2 pt-0.5">
        {stateLabel && <span className="hidden text-xs text-[var(--text-secondary)] sm:inline">{stateLabel}</span>}
        <Switch
          on={on}
          onChange={onChange}
          disabled={disabled}
          labelledBy={labelId}
          describedBy={hint ? hintId : undefined}
        />
      </div>
    </div>
  );
}

// Suffix renders a unit after a short numeric input ("%", "min", "Gi").
export function WithSuffix({ suffix, children }: { suffix: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2">
      <div className="w-28 shrink-0">{children}</div>
      <span className="text-xs text-[var(--text-secondary)]">{suffix}</span>
    </div>
  );
}

// Callout: an inline explanatory or warning block inside a card.
export function Callout({
  tone = "info",
  children,
}: {
  tone?: "info" | "warning" | "danger";
  children: React.ReactNode;
}) {
  return (
    <div
      className={cn(
        "rounded-md border px-3 py-2.5 text-xs leading-relaxed",
        tone === "info" && "border-[var(--border-subtle)] bg-[var(--bg-primary)]/50 text-[var(--text-secondary)]",
        tone === "warning" && "border-[var(--warning)]/35 bg-[var(--warning-subtle)] text-[var(--text-primary)]",
        tone === "danger" && "border-[var(--error)]/35 bg-[var(--error-subtle)] text-[var(--text-primary)]",
      )}
    >
      {children}
    </div>
  );
}

// Code renders an inline literal (path, key, host) inside sans prose.
export function Code({ children }: { children: React.ReactNode }) {
  return (
    <code className="rounded bg-[var(--bg-tertiary)] px-1 py-px font-mono text-xs text-[var(--text-primary)]">
      {children}
    </code>
  );
}
