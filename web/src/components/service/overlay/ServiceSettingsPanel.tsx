"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { toast } from "sonner";
import { usePatchService, type PatchServiceBody } from "@/features/services";
import { useCanOnProject, Perms } from "@/features/auth";
import { useEnvironments, setEnvGroupServiceBranch, envsQueryKey } from "@/features/projects";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { stripRepoCredentials } from "@/lib/format";
import type { KusoService } from "@/types/projects";
import { Github, Trash2, Network, Layers3, Hammer, Cloud, HardDrive, MapPin, ShieldAlert, Rocket, Moon, Activity } from "lucide-react";
import { cn } from "@/lib/utils";

import { useOverlayDirty } from "@/components/service/ServiceOverlay";
import { DiffConfirmDialog, type DiffEntry } from "@/components/shared/DiffConfirmDialog";
import { serviceBlast } from "@/lib/blast-radius";
import { fromSvc, isEqual, type FormState } from "./settings/_primitives";
import { releasePatch } from "./settings/releasePatch";
import { parseWatchPathsText } from "@/features/services/watchPaths";
import { SourceSection } from "./settings/SourceSection";
import { NetworkingSection } from "./settings/NetworkingSection";
import { ScaleSection } from "./settings/ScaleSection";
import { SleepSection } from "./settings/SleepSection";
import { PlacementSection } from "./settings/PlacementSection";
import { VolumesSection } from "./settings/VolumesSection";
import { BuildSection } from "./settings/BuildSection";
import { DeploySection } from "./settings/DeploySection";
import { ReleaseSection } from "./settings/ReleaseSection";
import { UptimeSection } from "./settings/UptimeSection";
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
  { id: "source",     label: "Source",     icon: Github },
  { id: "networking", label: "Networking", icon: Network },
  { id: "scale",      label: "Scale",      icon: Layers3 },
  { id: "sleep",      label: "Sleep",      icon: Moon },
  { id: "placement",  label: "Placement",  icon: MapPin },
  { id: "volumes",    label: "Volumes",    icon: HardDrive },
  { id: "build",      label: "Build",      icon: Hammer },
  { id: "deploy",     label: "Deploy",     icon: Cloud },
  { id: "release",    label: "Release",    icon: Rocket },
  { id: "uptime",     label: "Uptime",     icon: Activity },
  { id: "security",   label: "Security",   icon: ShieldAlert },
  { id: "danger",     label: "Danger",     icon: Trash2 },
] as const;

// fieldDiffValues maps a PatchServiceBody key back to a human-readable
// before/after pair, pulled from the real baseline vs current form
// state. Several body keys aggregate multiple FormState fields (scale,
// resources, repo, placement, image) so this can't be a naive
// key-lookup — each field gets a small formatter. Returns
// [before, after]; an empty string renders as "(unset)" / "(removed)"
// in DiffConfirmDialog.
function fieldDiffValues(
  field: string,
  base: FormState,
  next: FormState,
): [string, string] {
  const norm = (s: string) =>
    s.split("\n").map((x) => x.trim()).filter(Boolean).join(", ");
  const resources = (s: FormState) =>
    [
      s.cpuRequest && `cpu req ${s.cpuRequest}`,
      s.cpuLimit && `cpu lim ${s.cpuLimit}`,
      s.memRequest && `mem req ${s.memRequest}`,
      s.memLimit && `mem lim ${s.memLimit}`,
    ]
      .filter(Boolean)
      .join(", ");
  const scale = (s: FormState) =>
    [
      `min ${s.scaleMin}, max ${s.scaleMax}, cpu ${s.scaleCPU}%`,
      s.scaleUpWindow && `up delay ${s.scaleUpWindow}s`,
      s.scaleUpPods && `up step ${s.scaleUpPods} pods`,
      s.scaleUpPercent && `up step ${s.scaleUpPercent}%`,
      s.scaleDownWindow && `down delay ${s.scaleDownWindow}s`,
    ]
      .filter(Boolean)
      .join(", ");
  const repo = (s: FormState) =>
    [
      s.repoURL,
      s.repoBranch && `@${s.repoBranch}`,
      s.repoPath && `/${s.repoPath}`,
      s.repoProvider && `(${s.repoProvider})`,
      // The token is write-only and never shown, but flag its presence
      // in the "after" column so a token-only change doesn't render as
      // an identical before/after in the confirm dialog. `base` never
      // carries a token (baseline repoToken is always ""), so this only
      // ever decorates the changed side.
      s.repoToken.trim() && "· token updated",
    ]
      .filter(Boolean)
      .join(" ");
  const image = (s: FormState) =>
    [s.imageRepository, s.imageTag && `:${s.imageTag}`, s.imagePullSecret && ` (pull secret ${s.imagePullSecret})`]
      .filter(Boolean)
      .join("");
  const placement = (s: FormState) =>
    [
      s.placement
        .filter((r) => r.key.trim())
        .map((r) => `${r.key}=${r.value}`)
        .join(", "),
      s.placementNodes.filter(Boolean).length
        ? `nodes: ${s.placementNodes.filter(Boolean).join(", ")}`
        : "",
    ]
      .filter(Boolean)
      .join(" · ");
  const volumes = (s: FormState) =>
    s.volumes
      .filter((v) => v.name && v.mountPath)
      .map((v) => `${v.name}→${v.mountPath} (${v.sizeGi}Gi)`)
      .join(", ");
  const release = (s: FormState) =>
    s.releaseCommand.trim()
      ? `${s.releaseCommand.trim()}${s.releaseTimeout ? ` (${s.releaseTimeout}s)` : ""}`
      : "";
  const security = (s: FormState) =>
    [
      s.capAdd.trim() && `capAdd: ${s.capAdd.trim()}`,
      s.allowPrivilegeEscalation ? "allowPrivilegeEscalation: true" : "",
    ]
      .filter(Boolean)
      .join(", ");
  const pick: Record<string, (s: FormState) => string> = {
    displayName: (s) => s.displayName.trim(),
    port: (s) => s.port,
    domains: (s) => norm(s.domains),
    internal: (s) => (s.internal ? "internal (no public URL)" : "public"),
    requestLimits: (s) =>
      [
        s.limitMaxConcurrent && `max concurrent ${s.limitMaxConcurrent}`,
        s.limitRate && `rate ${s.limitRate}/s`,
        s.limitBurst && `burst ${s.limitBurst}`,
      ]
        .filter(Boolean)
        .join(", "),
    scale: scale,
    sleep: (s) =>
      [
        `production ${Number(s.scaleMin) === 0 || s.sleepEnabled ? "sleeps" : "always on"}`,
        `after ${s.sleepAfter}m`,
        s.sleepExcludePaths.trim() ? `keep-warm: ${s.sleepExcludePaths.split("\n").filter(Boolean).join(", ")}` : "",
      ]
        .filter(Boolean)
        .join(" · "),
    resources: resources,
    runtime: (s) => s.runtime,
    dockerfile: (s) => s.dockerfile,
    watchPaths: (s) => parseWatchPathsText(s.watchPaths).join(", ") || "default",
    image: image,
    repo: repo,
    placement: placement,
    volumes: volumes,
    previews: (s) => (s.previewsDisabled ? "disabled" : "enabled"),
    waitForCI: (s) => (s.waitForCI ? "wait for CI" : "build immediately"),
    uptime: (s) => (s.uptimeEnabled ? `checked at ${s.uptimePath.trim() || "default path"}` : "not checked"),
    release: release,
    securityContext: security,
  };
  const fmt = pick[field];
  if (!fmt) return ["current", "changed"];
  return [fmt(base), fmt(next)];
}

// ServiceSettingsPanel orchestrates the per-service settings overlay.
// Each section lives in ./settings/<Name>Section.tsx; this file owns
// the form state, the dirty/save bar, and the section-anchor nav.

// RFC 1123 hostname, optionally with a leading "*." wildcard label.
const HOSTNAME_RE = /^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{1,62}$/i;

export function ServiceSettingsPanel({ project, service, svc, env }: Props) {
  const onProduction = !env || env === "production";
  // Hoisted up from below so the env-domains save path inside onSave
  // can invalidate the envs cache after a successful PUT.
  const qcForPanel = useQueryClient();
  // Resolve the active env CR so we can show env-scoped state
  // (custom domains live here, not on the service spec). useEnvironments
  // is cached + shared with the canvas, so this doesn't add a network
  // round-trip beyond what the page already does.
  const envsForActive = useEnvironments(project);
  const activeEnv = useMemo(() => {
    const list = envsForActive.data ?? [];
    const matchesService = (e: typeof list[number]) =>
      e.spec.service === service ||
      e.spec.service === `${project}-${service}`;
    const envName = env || "production";
    return (
      list.find(
        (e) =>
          matchesService(e) &&
          (e.spec.kind === envName ||
            (e.metadata.labels &&
              e.metadata.labels["kuso.sislelabs.com/env"] === envName)),
      ) ?? list.find(matchesService)
    );
  }, [envsForActive.data, project, service, env]);
  // baseline = service spec (most fields) + env CR AdditionalHosts
  // (the domains list, which is per-env post-v0.16.19). The override
  // means the Networking section shows what's actually serving on
  // THIS env — staging's tickero.bg vs production's tickero.bg — and
  // saving routes through the env endpoint, not svc PATCH.
  const baseline = useMemo(() => {
    const fromService = fromSvc(svc);
    if (activeEnv) {
      const envHosts = activeEnv.spec.additionalHosts ?? [];
      return { ...fromService, domains: envHosts.join("\n") };
    }
    return fromService;
  }, [svc, activeEnv]);
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
  // Env-scoped custom domains staged alongside pendingBody. They save via
  // the env endpoint, not the service PATCH, but go through the same
  // confirm dialog (a domain change can hit the Let's Encrypt rate limit).
  const [pendingHosts, setPendingHosts] = useState<string[] | null>(null);
  const patch = usePatchService(project, service);
  // Pull the active env's host so the Networking section can
  // surface the auto-domain inline (read-only). The KusoService spec
  // doesn't carry the rendered hostname — that's stamped on the
  // KusoEnvironment at create time — so we have to reach over here.
  //
  // Scope to the env the user is actually viewing (env-group name from
  // the URL ?env= param, defaulted to "production"). Otherwise the
  // staging tab would show production.tickero.bg in the Networking
  // section because list.find() returned whichever env happened to be
  // indexed first.
  const envs = useEnvironments(project);
  const autoHost = useMemo(() => {
    const list = envs.data ?? [];
    const matchesService = (e: typeof list[number]) =>
      e.spec.service === service ||
      e.spec.service === `${project}-${service}`;
    const envName = env || "production";
    const forActiveEnv = list.find(
      (e) =>
        matchesService(e) &&
        (e.spec.kind === envName ||
          (e.metadata.labels &&
            e.metadata.labels["kuso.sislelabs.com/env"] === envName)),
    );
    // Fallback to ANY env of this service so legacy projects with no
    // kind label still get a host rendered.
    return (forActiveEnv ?? list.find(matchesService))?.spec.host;
  }, [envs.data, project, service, env]);
  // Gate the floating save bar on services:write — viewers can scroll
  // through the panel but can't edit. Inputs are still editable to
  // preserve copy/paste affordance, just not committable.
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);

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
    let envHosts: string[] | null = null;
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
        const bad = a.split("\n").filter(Boolean).find((h) => !HOSTNAME_RE.test(h));
        if (bad) {
          toast.error(`"${bad}" isn't a hostname (e.g. app.example.com; no scheme, path or port)`);
          return;
        }
        // Per-env scope (v0.16.19): the form binds to the env CR's
        // AdditionalHosts, and saves go to the env endpoint so the
        // change doesn't leak to sibling envs. Staged here; applyPatch
        // PUTs them after the confirm dialog.
        if (activeEnv) {
          envHosts = a.split("\n").filter(Boolean);
        } else {
          // No active env CR resolved (legacy fallback): use the old
          // svc-level path. spec.domains becomes a seed-only template
          // post-v0.16.19, so this is harmless for new envs.
          body.domains = a
            .split("\n")
            .filter(Boolean)
            .map((host) => ({ host, tls: true }));
        }
      }
    }
    if (state.internal !== baseline.internal) {
      body.internal = state.internal;
    }
    // Blank clears the override (0); -1 means no limit.
    const limitFields = [
      ["maxConcurrent", "limitMaxConcurrent", -1],
      ["ratePerSecond", "limitRate", -1],
      ["burst", "limitBurst", 0],
    ] as const;
    for (const [key, field, lo] of limitFields) {
      const raw = state[field].trim();
      if (raw === baseline[field].trim()) continue;
      const n = raw === "" ? 0 : Number(raw);
      if (!Number.isInteger(n) || n < lo || n > 1000000) {
        toast.error(`${key} must be a whole number${lo < 0 ? ", or -1 for no limit" : ""}`);
        return;
      }
      body.requestLimits = { ...body.requestLimits, [key]: n };
    }
    const scaleChanged =
      state.scaleMin !== baseline.scaleMin ||
      state.scaleMax !== baseline.scaleMax ||
      state.scaleCPU !== baseline.scaleCPU ||
      state.scaleUpWindow !== baseline.scaleUpWindow ||
      state.scaleUpPods !== baseline.scaleUpPods ||
      state.scaleUpPercent !== baseline.scaleUpPercent ||
      state.scaleDownWindow !== baseline.scaleDownWindow;
    const excludeChanged = state.sleepExcludePaths !== baseline.sleepExcludePaths;
    const nonProdChanged = state.sleepNonProduction !== baseline.sleepNonProduction;
    const sleepChanged =
      state.sleepEnabled !== baseline.sleepEnabled ||
      state.sleepAfter !== baseline.sleepAfter ||
      nonProdChanged;
    if (scaleChanged || excludeChanged || sleepChanged) {
      const min = Number(state.scaleMin);
      const max = Number(state.scaleMax);
      const cpu = Number(state.scaleCPU);
      if (min < 0 || max < Math.max(min, 1)) {
        toast.error("max must be ≥ max(min, 1) and min ≥ 0");
        return;
      }
      if (scaleChanged) {
        body.scale = { min, max, targetCPU: cpu };
        // Blank = back to the default: -1 for the delays (0 is a real
        // value there), 0 for the step.
        const speed = [
          ["scaleUpStabilizationSeconds", "scaleUpWindow", -1, 0, 3600],
          ["scaleUpPods", "scaleUpPods", 0, 1, 100],
          ["scaleUpPercent", "scaleUpPercent", 0, 1, 1000],
          ["scaleDownStabilizationSeconds", "scaleDownWindow", -1, 0, 3600],
        ] as const;
        for (const [key, field, reset, lo, hi] of speed) {
          const raw = state[field].trim();
          if (raw === baseline[field].trim()) continue;
          const n = raw === "" ? reset : Number(raw);
          if (raw !== "" && (!Number.isInteger(n) || n < lo || n > hi)) {
            toast.error(`${key} must be a whole number from ${lo} to ${hi}`);
            return;
          }
          body.scale[key] = n;
        }
      }
      // min=0 only works behind the activator, so it forces production
      // sleep on. Otherwise sleep.enabled follows the Sleep switch; it used
      // to be reset to false on every scale save with min ≥ 1, silently
      // undoing sleep enabled elsewhere (CLI, kuso.yml).
      const minCrossedZero = (Number(baseline.scaleMin) === 0) !== (min === 0);
      if (sleepChanged || excludeChanged || minCrossedZero) {
        body.sleep = { enabled: min === 0 || state.sleepEnabled };
        if (state.sleepAfter !== baseline.sleepAfter) {
          const after = Number(state.sleepAfter);
          if (!Number.isInteger(after) || after < 1) {
            toast.error("Idle window must be a whole number of minutes ≥ 1");
            return;
          }
          body.sleep.afterMinutes = after;
        }
        if (nonProdChanged) {
          body.sleep.nonProduction = state.sleepNonProduction === "off" ? "off" : "on";
        }
        if (excludeChanged) {
          // Paths that must stay reachable (webhooks/callbacks) keep the
          // whole deployment warm. An empty list clears the override.
          const paths = state.sleepExcludePaths
            .split("\n")
            .map((p) => p.trim())
            .filter(Boolean);
          body.sleep.wakeOn = paths.length > 0 ? { excludePaths: paths } : { clear: true };
        }
      }
    }
    if (
      state.cpuRequest !== baseline.cpuRequest ||
      state.cpuLimit !== baseline.cpuLimit ||
      state.memRequest !== baseline.memRequest ||
      state.memLimit !== baseline.memLimit
    ) {
      // Build a k8s ResourceRequirements map, omitting blank fields.
      // All-blank → send an empty object to CLEAR resources (chart
      // default). The server validates quantities at apply time.
      const req: Record<string, string> = {};
      const lim: Record<string, string> = {};
      if (state.cpuRequest.trim()) req.cpu = state.cpuRequest.trim();
      if (state.memRequest.trim()) req.memory = state.memRequest.trim();
      if (state.cpuLimit.trim()) lim.cpu = state.cpuLimit.trim();
      if (state.memLimit.trim()) lim.memory = state.memLimit.trim();
      const resources: Record<string, unknown> = {};
      if (Object.keys(req).length) resources.requests = req;
      if (Object.keys(lim).length) resources.limits = lim;
      body.resources = resources;
    }
    if (state.runtime !== baseline.runtime) {
      body.runtime = state.runtime;
    }
    // dockerfile path: only meaningful for runtime=dockerfile. Send on
    // change ("" clears back to the default "Dockerfile" server-side).
    if (state.dockerfile !== baseline.dockerfile) {
      body.dockerfile = state.dockerfile;
    }
    if (state.watchPaths !== baseline.watchPaths) {
      body.watchPaths = parseWatchPathsText(state.watchPaths);
    }
    // Image reference: only meaningful for runtime=image services,
    // where re-pointing repository/tag + saving is the redeploy path
    // (they never build). Repository must stay non-empty — clearing
    // it would strand the service with nothing to run.
    if (
      state.imageRepository !== baseline.imageRepository ||
      state.imageTag !== baseline.imageTag ||
      state.imagePullSecret !== baseline.imagePullSecret
    ) {
      const repository = state.imageRepository.trim();
      if (!repository) {
        toast.error("Image repository can't be empty");
        return;
      }
      body.image = {
        repository,
        tag: state.imageTag.trim() || undefined,
        pullSecret: state.imagePullSecret,
      };
    }
    // A typed GitLab token is write-only — it never round-trips from the
    // server, so it can't be diffed against the baseline the way the
    // other repo fields are. Treat a non-empty token as its own reason
    // to send the repo patch, in addition to url/branch/path/provider
    // edits.
    const tokenToSend = state.repoToken.trim();
    if (
      state.repoURL !== baseline.repoURL ||
      state.repoBranch !== baseline.repoBranch ||
      state.repoPath !== baseline.repoPath ||
      state.repoInstallationID !== baseline.repoInstallationID ||
      state.repoProvider !== baseline.repoProvider ||
      tokenToSend
    ) {
      body.repo = {
        url: state.repoURL,
        branch: state.repoBranch || undefined,
        path: state.repoPath || undefined,
        // 0 → omit so the server doesn't clobber the existing
        // installationId with "unset"; only send when explicitly
        // changed by the picker.
        installationId: state.repoInstallationID || undefined,
        // Only send an explicit provider override; "" means "let the
        // server infer from the URL host" (its authoritative detection).
        provider:
          state.repoProvider === "gitlab" || state.repoProvider === "github"
            ? state.repoProvider
            : undefined,
        // WRITE-ONLY GitLab clone credential. Only send when the user
        // actually typed one — a blank field must NOT clear an existing
        // stored token, so we omit it entirely when empty.
        token: tokenToSend || undefined,
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
      // The server merges `disabled` into spec.previews; {clear:true}
      // would also wipe seed/reviewUrl/previewEnvVars.
      body.previews = { disabled: state.previewsDisabled };
    }
    if (state.waitForCI !== baseline.waitForCI) {
      body.waitForCI = state.waitForCI;
    }
    {
      const path = state.uptimePath.trim();
      const pathChanged = path !== baseline.uptimePath;
      if (pathChanged && path) {
        // Mirrors the server's rule so a bad path fails here, not as a 400.
        if (!path.startsWith("/") || path.startsWith("//") || /\s/.test(path) || path.length > 512) {
          toast.error("Check path must start with a single / and contain no spaces (≤512 chars)");
          return;
        }
      }
      if (pathChanged || state.uptimeEnabled !== baseline.uptimeEnabled) {
        body.uptime = {
          ...(state.uptimeEnabled !== baseline.uptimeEnabled ? { disabled: !state.uptimeEnabled } : {}),
          ...(pathChanged ? { path } : {}),
        };
      }
    }
    {
      const release = releasePatch(
        state.releaseCommand,
        state.releaseTimeout,
        baseline.releaseCommand,
        baseline.releaseTimeout,
        svc?.spec.release?.command,
      );
      if (release) body.release = release;
    }
    if (
      state.capAdd !== baseline.capAdd ||
      state.allowPrivilegeEscalation !== baseline.allowPrivilegeEscalation
    ) {
      const caps = state.capAdd
        .split(",")
        .map((c) => c.trim())
        .filter(Boolean);
      // Server semantics: nil = leave alone, non-nil = set verbatim. We
      // only get here when the user changed a field, so always send: an
      // emptied block (no caps, escalation off) renders exactly kuso's
      // hardened default, which is how the user clears an override.
      body.securityContext = {
        ...(caps.length > 0 ? { capabilities: { add: caps } } : {}),
        allowPrivilegeEscalation: state.allowPrivilegeEscalation,
      };
    }

    if (Object.keys(body).length === 0 && envHosts === null) {
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
    setPendingHosts(envHosts);
    setPendingBody(body);
  };

  // applyPatch commits the body the confirm dialog is showing.
  const applyPatch = async (body: PatchServiceBody, hosts: string[] | null) => {
    setPending(true);
    setSaveError(null);
    try {
      // Domains first so a failure (e.g. cross-env conflict) surfaces
      // before other service fields are partially applied.
      if (hosts !== null) {
        const envName = env || "production";
        try {
          await api<unknown>(
            `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/envs/${encodeURIComponent(envName)}/domains`,
            { method: "PUT", body: { hosts } },
          );
        } catch (err) {
          throw new Error(`Save domains: ${err instanceof Error ? err.message : "failed"}`);
        }
        // Refetch so the baseline picks up the new AdditionalHosts and
        // the dirty flag clears.
        await qcForPanel.invalidateQueries({ queryKey: envsQueryKey(project) });
        // Canvas tiles / project card read the URL from the describe payload.
        qcForPanel.invalidateQueries({ queryKey: ["projects", project], exact: true });
      }
      if (Object.keys(body).length > 0) {
        await patch.mutateAsync(body);
      }
      toast.success("Changes saved");
      setPendingBody(null);
      setPendingHosts(null);
      // The GitLab token is write-only — the server took it into a
      // Secret and will never echo it back, so clear the field now that
      // it's committed. Without this the typed value lingers in state
      // while the refetched baseline has repoToken="", leaving the form
      // permanently "dirty" and the save bar stuck open.
      if (body.repo?.token) {
        setState((s) => ({ ...s, repoToken: "" }));
      }
    } catch (e) {
      const msg = e instanceof Error ? e.message : "Failed to save";
      // Both surfaces: toast for momentary visibility, inline
      // saveError for "where did my changes go" recovery.
      toast.error(msg);
      setSaveError(msg);
      setPendingBody(null);
      setPendingHosts(null);
    } finally {
      setPending(false);
    }
  };

  // diffEntries turns the pending patch body into the confirm
  // dialog's row list, each tagged with its EDIT_SAFETY blast radius.
  // The before/after values are read from the real baseline vs state
  // (NOT the patch body — several body keys are reshaped, e.g. `scale`
  // spans three form fields) so the user sees the actual change per
  // field, mirroring how EnvVarsEditor builds its diff.
  const diffEntries: DiffEntry[] = pendingBody
    ? [...Object.keys(pendingBody), ...(pendingHosts !== null ? ["domains"] : [])].map((field) => {
        const [before, after] = fieldDiffValues(field, baseline, state);
        return {
          field,
          before,
          after,
          warning: serviceBlast(field) ?? undefined,
        };
      })
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

  // Uptime checks ping the HTTP port, so workers and internal-only
  // services have nothing to check.
  const uptimeApplies = state.runtime !== "worker" && !state.internal;
  const sections = SECTIONS.filter((s) => s.id !== "uptime" || uptimeApplies);

  return (
    <div className="relative">
      {/* On md+ the layout is a 2-col grid with a sticky sidebar
          jump-nav. On smaller screens the sidebar would steal half
          the width from the actual form, so we collapse to a single
          column + a horizontal-scroll chip strip pinned to the top.
          That keeps the section anchors discoverable on phones
          without crowding the inputs. */}
      <nav className="sticky top-0 z-10 -mx-px flex gap-1 overflow-x-auto border-b border-[var(--border-subtle)] bg-[var(--bg-primary)]/95 px-3 py-2 text-xs backdrop-blur md:hidden">
        {sections.map((s) => (
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
      <div className="grid grid-cols-1 gap-0 pb-24 md:grid-cols-[minmax(0,1fr)_180px]">
        <div className="min-w-0 space-y-8 px-4 py-4 md:px-6 md:py-6">
          {!onProduction && env && (
            <EnvBranchSection project={project} env={env} service={service} svc={svc} />
          )}
          <SourceSection state={state} setState={setState} project={project} service={service} />
          <NetworkingSection state={state} setState={setState} autoHost={autoHost} envName={env || "production"} />
          <ScaleSection state={state} setState={setState} />
          <SleepSection state={state} setState={setState} />
          <PlacementSection state={state} setState={setState} />
          <VolumesSection state={state} setState={setState} />
          <BuildSection state={state} setState={setState} project={project} />
          <DeploySection project={project} state={state} setState={setState} />
          <ReleaseSection state={state} setState={setState} />
          {uptimeApplies && <UptimeSection project={project} state={state} setState={setState} />}
          <SecuritySection state={state} setState={setState} />
          <DangerSection project={project} service={service} />
        </div>

        <nav className="sticky top-0 hidden self-start px-4 py-6 text-sm md:block">
          <ul className="space-y-2">
            {sections.map((s) => (
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
        onCancel={() => {
          setPendingBody(null);
          setPendingHosts(null);
        }}
        onConfirm={() => {
          if (pendingBody) void applyPatch(pendingBody, pendingHosts);
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
  // The env CR's spec.branch is the OVERRIDE; empty means "no override,
  // fall back to the service default branch". Keep these separate so we
  // can (a) tell whether an override is actually set, and (b) prefill a
  // sensible suggestion when it isn't.
  const overrideBranch = envRow?.spec.branch ?? "";
  const serviceDefaultBranch = svc?.spec?.repo?.defaultBranch ?? "main";
  const hasOverride = overrideBranch.trim() !== "";
  // When no override is set, the env's own name (the `staging`-env-
  // tracks-`staging`-branch convention) is shown as the placeholder, not
  // the value, so the input never looks like a live setting it isn't.
  const suggestedBranch = env;
  const currentBranch = overrideBranch;
  const repoLabel = (() => {
    // Credentials out FIRST: a gitlab deploy-token URL fell through the
    // github-only match below and rendered the token verbatim.
    const url = stripRepoCredentials(svc?.spec?.repo?.url ?? "");
    if (!url) return "";
    const m = url.match(/(?:github|gitlab)\.com[/:]([^/]+\/[^/.]+)/i);
    return m ? m[1] : url;
  })();
  const [branch, setBranch] = useState(currentBranch);
  useEffect(() => {
    setBranch(currentBranch);
  }, [currentBranch]);
  // "Dirty" = the input differs from what's actually persisted. Empty
  // stays unsavable: the server rejects an empty branch, so there is no
  // "clear override" call yet.
  const dirty = branch.trim() !== "" && branch.trim() !== overrideBranch.trim();
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
      qc.invalidateQueries({ queryKey: ["projects", project], exact: true });
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
      <div className="flex items-center gap-2 px-3 pt-3">
        <input
          type="text"
          value={branch}
          onChange={(e) => setBranch(e.target.value)}
          placeholder={suggestedBranch}
          aria-label={`Branch ${service} tracks in ${env}`}
          spellCheck={false}
          className="h-8 flex-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px] outline-none focus:border-[var(--accent)]"
        />
        <Button size="sm" disabled={!dirty || saving} onClick={save}>
          {saving ? "Saving…" : "Save branch"}
        </Button>
      </div>
      {/* Inline resolution so the default-vs-override relationship is
          legible: spell out which branch this env effectively deploys
          and what the service default is. */}
      <p className="px-3 pb-3 pt-1.5 text-[11px] text-[var(--text-secondary)]">
        {hasOverride ? (
          <>
            <span className="font-mono text-blue-200">{env}</span> deploys{" "}
            <span className="font-mono">{overrideBranch}</span>{" "}
            <span className="text-[var(--text-tertiary)]">(override)</span> · service default is{" "}
            <span className="font-mono">{serviceDefaultBranch}</span>
          </>
        ) : (
          <>
            No override — <span className="font-mono text-blue-200">{env}</span> falls back to the
            service default{" "}
            <span className="font-mono">{serviceDefaultBranch}</span>.{" "}
            {branch.trim() && branch.trim() !== serviceDefaultBranch ? (
              <>
                Save to track{" "}
                <span className="font-mono">{branch.trim()}</span> here instead.
              </>
            ) : (
              <>
                Suggested:{" "}
                <span className="font-mono">{suggestedBranch}</span>.
              </>
            )}
          </>
        )}
      </p>
    </section>
  );
}
