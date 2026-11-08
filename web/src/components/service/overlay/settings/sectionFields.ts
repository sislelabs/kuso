import type { FormState } from "./_primitives";

// Every FormState field belongs to exactly one settings section, so each
// section card can track its own dirty state and save only its fields.
// sectionFields.test.ts fails when a new FormState field isn't mapped.
export const SECTION_FIELDS = {
  source: [
    "displayName",
    "repoURL",
    "repoBranch",
    "repoPath",
    "repoInstallationID",
    "repoProvider",
    "repoToken",
    "watchPaths",
  ],
  networking: [
    "port",
    "internal",
    "privateEgress",
    "platformApiEgress",
    "limitMaxConcurrent",
    "limitRate",
    "limitBurst",
  ],
  scale: [
    "scaleMin",
    "scaleMax",
    "scaleCPU",
    "scaleUpWindow",
    "scaleUpPods",
    "scaleUpPercent",
    "scaleDownWindow",
    "cpuRequest",
    "cpuLimit",
    "memRequest",
    "memLimit",
  ],
  sleep: ["sleepEnabled", "sleepAfter", "sleepNonProduction", "sleepExcludePaths"],
  placement: ["placement", "placementNodes"],
  volumes: ["volumes"],
  build: [
    "runtime",
    "dockerfile",
    "imageRepository",
    "imageTag",
    "imagePullSecret",
    "buildArgs",
    "publicEnv",
    "staticBuilderImage",
    "staticRuntimeImage",
    "staticBuildCmd",
    "staticOutputDir",
    "buildpacksBuilderImage",
    "buildpacksLifecycleImage",
  ],
  deploy: ["startCommand", "waitForCI", "previewsDisabled"],
  release: ["releaseCommand", "releaseTimeout", "snapshotBeforeDeploy"],
  healthcheck: ["hcEnabled", "hcPath", "hcPort", "hcDelay", "hcPeriod", "hcTimeout", "hcThreshold"],
  uptime: ["uptimeEnabled", "uptimePath"],
  security: ["capAdd", "allowPrivilegeEscalation"],
} as const satisfies Record<string, readonly (keyof FormState)[]>;

export type SaveableSection = keyof typeof SECTION_FIELDS;

export function isSaveableSection(id: string): id is SaveableSection {
  return Object.prototype.hasOwnProperty.call(SECTION_FIELDS, id);
}

function sameValue(a: unknown, b: unknown): boolean {
  return a === b || JSON.stringify(a) === JSON.stringify(b);
}

export function isSectionDirty(section: SaveableSection, state: FormState, baseline: FormState): boolean {
  return SECTION_FIELDS[section].some((f) => !sameValue(state[f], baseline[f]));
}

export function dirtySections(state: FormState, baseline: FormState): SaveableSection[] {
  return (Object.keys(SECTION_FIELDS) as SaveableSection[]).filter((s) => isSectionDirty(s, state, baseline));
}

// scopedState is the baseline with only `sections`' fields taken from the
// edited state: saving one card must not also send another card's edits.
export function scopedState(
  sections: readonly SaveableSection[],
  state: FormState,
  baseline: FormState,
): FormState {
  const out: FormState = { ...baseline };
  const target = out as unknown as Record<string, unknown>;
  const src = state as unknown as Record<string, unknown>;
  for (const s of sections) {
    for (const f of SECTION_FIELDS[s]) target[f] = src[f];
  }
  return out;
}

// discardSection restores one card's fields to the baseline and keeps
// every other card's edits.
export function discardSection(section: SaveableSection, state: FormState, baseline: FormState): FormState {
  const out: FormState = { ...state };
  const target = out as unknown as Record<string, unknown>;
  const base = baseline as unknown as Record<string, unknown>;
  for (const f of SECTION_FIELDS[section]) target[f] = base[f];
  return out;
}
