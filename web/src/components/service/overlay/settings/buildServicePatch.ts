import { parseWatchPathsText } from "@/features/services/watchPaths";
import { imageRefString, imageRepositoryProblem, imageTagProblem, specImageTag } from "@/features/services/imageRef";
import type { FormState } from "./_primitives";
import { releasePatch } from "./releasePatch";
import { extraServicePatch } from "./servicePatchExtras";
import type { PatchServiceBody } from "@/features/services";
import { describeHealthcheck, healthcheckPatch } from "./healthcheckPatch";

// buildServicePatch turns the edited form into the PATCH body, sending only
// fields that differ from the baseline. Returns the first validation error
// instead when a changed field is invalid. `command`/`releaseCommand` are
// the stored argv, needed to tell "unchanged" from "re-split".
export function buildServicePatch(
  state: FormState,
  baseline: FormState,
  stored: { command?: string[]; releaseCommand?: string[] },
): { body: PatchServiceBody } | { error: string } {
  const body: PatchServiceBody = {};
  if (state.displayName !== baseline.displayName) {
    const trimmed = state.displayName.trim();
    if (trimmed && !/^[A-Za-z0-9 -]{1,60}$/.test(trimmed)) {
      return { error: "Display name: letters, digits, spaces and hyphens only, up to 60 characters" };
    }
    body.displayName = trimmed;
  }
  const portNum = Number(state.port);
  if (portNum !== Number(baseline.port)) {
    if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
      return { error: "Port must be between 1 and 65535" };
    }
    body.port = portNum;
  }
  if (state.internal !== baseline.internal) {
    body.internal = state.internal;
  }
  // Blank clears the override (0); -1 means no limit.
  const limitFields = [
    ["maxConcurrent", "limitMaxConcurrent", -1, "Max concurrent requests"],
    ["ratePerSecond", "limitRate", -1, "Rate limit"],
    ["burst", "limitBurst", 0, "Burst"],
  ] as const;
  for (const [key, field, lo, label] of limitFields) {
    const raw = state[field].trim();
    if (raw === baseline[field].trim()) continue;
    const n = raw === "" ? 0 : Number(raw);
    if (!Number.isInteger(n) || n < lo || n > 1000000) {
      return { error: `${label} must be a whole number${lo < 0 ? ", or -1 for no limit" : ""}` };
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
    state.sleepEnabled !== baseline.sleepEnabled || state.sleepAfter !== baseline.sleepAfter || nonProdChanged;
  if (scaleChanged || excludeChanged || sleepChanged) {
    const min = Number(state.scaleMin);
    const max = Number(state.scaleMax);
    const cpu = Number(state.scaleCPU);
    if (!Number.isInteger(min) || min < 0 || !Number.isInteger(max) || max < Math.max(min, 1)) {
      return { error: "Replicas: min must be 0 or more, and max at least min (and at least 1)" };
    }
    if (scaleChanged) {
      body.scale = { min, max, targetCPU: cpu };
      // Blank = back to the default: -1 for the delays (0 is a real
      // value there), 0 for the step.
      const speed = [
        ["scaleUpStabilizationSeconds", "scaleUpWindow", -1, 0, 3600, "Scale-up delay"],
        ["scaleUpPods", "scaleUpPods", 0, 1, 100, "Scale-up pods"],
        ["scaleUpPercent", "scaleUpPercent", 0, 1, 1000, "Scale-up percent"],
        ["scaleDownStabilizationSeconds", "scaleDownWindow", -1, 0, 3600, "Scale-down delay"],
      ] as const;
      for (const [key, field, reset, lo, hi, label] of speed) {
        const raw = state[field].trim();
        if (raw === baseline[field].trim()) continue;
        const n = raw === "" ? reset : Number(raw);
        if (raw !== "" && (!Number.isInteger(n) || n < lo || n > hi)) {
          return { error: `${label} must be a whole number from ${lo} to ${hi}` };
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
          return { error: "Idle window must be a whole number of minutes, at least 1" };
        }
        body.sleep.afterMinutes = after;
      }
      if (nonProdChanged) {
        body.sleep.nonProduction = state.sleepNonProduction === "off" ? "off" : "on";
      }
      if (excludeChanged) {
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
    // All-blank sends an empty object, which clears resources back to the
    // chart default. The server validates quantities at apply time.
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
  // "" clears back to the default "Dockerfile" server-side.
  if (state.dockerfile !== baseline.dockerfile) {
    body.dockerfile = state.dockerfile;
  }
  if (state.watchPaths !== baseline.watchPaths) {
    body.watchPaths = parseWatchPathsText(state.watchPaths);
  }
  if (
    state.imageRepository !== baseline.imageRepository ||
    state.imageTag !== baseline.imageTag ||
    state.imagePullSecret !== baseline.imagePullSecret
  ) {
    // Clearing the repository would strand the service with nothing to run.
    const repository = state.imageRepository.trim();
    if (!repository) return { error: "Image repository can't be empty" };
    const problem = imageRepositoryProblem(repository) ?? imageTagProblem(state.imageTag);
    if (problem) return { error: problem };
    body.image = {
      repository,
      tag: specImageTag(state.imageTag),
      pullSecret: state.imagePullSecret,
    };
  }
  // The repo token is write-only (never round-trips), so a typed token is
  // its own reason to send the repo patch.
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
      // 0 → omit so the server keeps the existing installationId.
      installationId: state.repoInstallationID || undefined,
      // "" lets the server infer the provider from the URL host.
      provider: state.repoProvider === "gitlab" || state.repoProvider === "github" ? state.repoProvider : undefined,
      // Blank must NOT clear a stored token, so omit it entirely.
      token: tokenToSend || undefined,
    };
  }
  if (
    JSON.stringify({ p: state.placement, n: state.placementNodes }) !==
    JSON.stringify({ p: baseline.placement, n: baseline.placementNodes })
  ) {
    const labels: Record<string, string> = {};
    for (const r of state.placement) {
      if (!r.key.trim()) continue;
      labels[r.key.trim()] = r.value;
    }
    const nodes = state.placementNodes.filter(Boolean);
    body.placement =
      Object.keys(labels).length === 0 && nodes.length === 0 ? { clear: true } : { labels, nodes };
  }
  if (JSON.stringify(state.volumes) !== JSON.stringify(baseline.volumes)) {
    for (const v of state.volumes) {
      if (!v.name || !v.mountPath) continue;
      if (!/^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$/.test(v.name)) {
        return { error: `Volume name "${v.name}": lowercase letters, digits and dashes, up to 32 characters` };
      }
      if (!v.mountPath.startsWith("/")) {
        return { error: `Mount path "${v.mountPath}" must start with /` };
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
    // Mirrors the server's rule so a bad path fails here, not as a 400.
    if (pathChanged && path && (!path.startsWith("/") || path.startsWith("//") || /\s/.test(path) || path.length > 512)) {
      return { error: "Check path must start with a single / and contain no spaces (up to 512 characters)" };
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
      stored.releaseCommand,
    );
    if (release) body.release = release;
  }
  if (state.capAdd !== baseline.capAdd || state.allowPrivilegeEscalation !== baseline.allowPrivilegeEscalation) {
    const caps = state.capAdd
      .split(",")
      .map((c) => c.trim())
      .filter(Boolean);
    // Server semantics: nil = leave alone, non-nil = set verbatim. An
    // emptied block renders kuso's hardened default.
    body.securityContext = {
      ...(caps.length > 0 ? { capabilities: { add: caps } } : {}),
      allowPrivilegeEscalation: state.allowPrivilegeEscalation,
    };
  }
  const hc = healthcheckPatch(state, baseline);
  if ("error" in hc) return { error: hc.error };
  if (hc.healthcheck) body.healthcheck = hc.healthcheck;
  const extra = extraServicePatch(state, baseline, stored.command);
  if ("error" in extra) return { error: extra.error };
  Object.assign(body, extra.body);
  return { body };
}

// fieldDiffValues maps a PatchServiceBody key back to a readable
// before/after pair for the confirm dialog. Several body keys aggregate
// multiple form fields, so each gets a small formatter. "" renders as
// "(unset)" / "(removed)" in DiffConfirmDialog.
export function fieldDiffValues(field: string, base: FormState, next: FormState): [string, string] {
  const norm = (s: string) =>
    s
      .split("\n")
      .map((x) => x.trim())
      .filter(Boolean)
      .join(", ");
  const pick: Record<string, (s: FormState) => string> = {
    displayName: (s) => s.displayName.trim(),
    port: (s) => s.port,
    internal: (s) => (s.internal ? "internal (no public URL)" : "public"),
    requestLimits: (s) =>
      [
        s.limitMaxConcurrent && `max concurrent ${s.limitMaxConcurrent}`,
        s.limitRate && `rate ${s.limitRate}/s`,
        s.limitBurst && `burst ${s.limitBurst}`,
      ]
        .filter(Boolean)
        .join(", "),
    scale: (s) =>
      [
        `min ${s.scaleMin}, max ${s.scaleMax}, cpu ${s.scaleCPU}%`,
        s.scaleUpWindow && `up delay ${s.scaleUpWindow}s`,
        s.scaleUpPods && `up step ${s.scaleUpPods} pods`,
        s.scaleUpPercent && `up step ${s.scaleUpPercent}%`,
        s.scaleDownWindow && `down delay ${s.scaleDownWindow}s`,
      ]
        .filter(Boolean)
        .join(", "),
    sleep: (s) =>
      [
        `production ${Number(s.scaleMin) === 0 || s.sleepEnabled ? "sleeps" : "always on"}`,
        `after ${s.sleepAfter}m`,
        s.sleepExcludePaths.trim() ? `keep-warm: ${norm(s.sleepExcludePaths)}` : "",
      ]
        .filter(Boolean)
        .join(" · "),
    resources: (s) =>
      [
        s.cpuRequest && `cpu req ${s.cpuRequest}`,
        s.cpuLimit && `cpu lim ${s.cpuLimit}`,
        s.memRequest && `mem req ${s.memRequest}`,
        s.memLimit && `mem lim ${s.memLimit}`,
      ]
        .filter(Boolean)
        .join(", "),
    runtime: (s) => s.runtime,
    dockerfile: (s) => s.dockerfile,
    watchPaths: (s) => parseWatchPathsText(s.watchPaths).join(", ") || "default",
    image: (s) =>
      [imageRefString(s.imageRepository, s.imageTag), s.imagePullSecret && ` (pull secret ${s.imagePullSecret})`]
        .filter(Boolean)
        .join(""),
    repo: (s) =>
      [
        s.repoURL,
        s.repoBranch && `@${s.repoBranch}`,
        s.repoPath && `/${s.repoPath}`,
        s.repoProvider && `(${s.repoProvider})`,
        // The baseline never carries a token, so this only marks the
        // changed side and a token-only edit isn't an identical pair.
        s.repoToken.trim() && "· token updated",
      ]
        .filter(Boolean)
        .join(" "),
    placement: (s) =>
      [
        s.placement
          .filter((r) => r.key.trim())
          .map((r) => `${r.key}=${r.value}`)
          .join(", "),
        s.placementNodes.filter(Boolean).length ? `nodes: ${s.placementNodes.filter(Boolean).join(", ")}` : "",
      ]
        .filter(Boolean)
        .join(" · "),
    volumes: (s) =>
      s.volumes
        .filter((v) => v.name && v.mountPath)
        .map(
          (v) =>
            `${v.name}→${v.mountPath} (${[`${v.sizeGi}Gi`, v.accessMode, v.storageClass].filter(Boolean).join(", ")})`,
        )
        .join(", "),
    previews: (s) => (s.previewsDisabled ? "disabled" : "enabled"),
    waitForCI: (s) => (s.waitForCI ? "wait for CI" : "build immediately"),
    healthcheck: describeHealthcheck,
    uptime: (s) => (s.uptimeEnabled ? `checked at ${s.uptimePath.trim() || "default path"}` : "not checked"),
    release: (s) =>
      s.releaseCommand.trim()
        ? `${s.releaseCommand.trim()}${s.releaseTimeout ? ` (${s.releaseTimeout}s)` : ""}`
        : "",
    securityContext: (s) =>
      [s.capAdd.trim() && `capAdd: ${s.capAdd.trim()}`, s.allowPrivilegeEscalation ? "allowPrivilegeEscalation: true" : ""]
        .filter(Boolean)
        .join(", "),
    privateEgress: (s) => (s.privateEgress ? "namespace-only egress" : "internet egress"),
    platformApiEgress: (s) => (s.platformApiEgress ? "can reach the kuso API" : "no kuso API access"),
    snapshotBeforeDeploy: (s) => (s.snapshotBeforeDeploy ? "snapshot before deploy" : "no snapshot"),
    command: (s) => s.startCommand.trim(),
    buildArgs: (s) => norm(s.buildArgs.replace(/=.*$/gm, "")),
    publicEnv: (s) => norm(s.publicEnv),
    static: (s) =>
      [
        s.staticBuildCmd && `build: ${s.staticBuildCmd}`,
        s.staticOutputDir && `output: ${s.staticOutputDir}`,
        s.staticBuilderImage && `builder: ${s.staticBuilderImage}`,
        s.staticRuntimeImage && `runtime: ${s.staticRuntimeImage}`,
      ]
        .filter(Boolean)
        .join(", "),
    buildpacks: (s) =>
      [
        s.buildpacksBuilderImage && `builder: ${s.buildpacksBuilderImage}`,
        s.buildpacksLifecycleImage && `lifecycle: ${s.buildpacksLifecycleImage}`,
      ]
        .filter(Boolean)
        .join(", "),
  };
  const fmt = pick[field];
  if (!fmt) return ["current", "changed"];
  return [fmt(base), fmt(next)];
}
