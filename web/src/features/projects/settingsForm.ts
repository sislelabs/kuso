import type { KusoProject } from "@/types/projects";
import type { UpdateProjectBody } from "./mutations";

// Project settings are edited as independent cards, each saved on its
// own. A card only ever sends the fields it owns, and only the ones that
// changed from what the form was seeded with, so a stale form can't
// revert a value someone else (or a kuso.yaml apply) set meanwhile.

export interface ProjectSettingsForm {
  description: string;
  baseDomain: string;
  repoURL: string;
  repoBranch: string;
  // 0 = no GitHub App installation.
  installationId: number;
  previewsEnabled: boolean;
  // Raw input strings so a field can be emptied while typing.
  previewsTtl: string;
  previewsDomain: string;
  uptimeEnabled: boolean;
  alwaysOn: boolean;
  limitMaxConcurrent: string;
  limitRate: string;
  limitBurst: string;
}

export type ProjectSettingsSection = "general" | "previews" | "monitoring" | "scaling";

export const PROJECT_SETTINGS_FIELDS: Record<ProjectSettingsSection, (keyof ProjectSettingsForm)[]> = {
  general: ["description", "baseDomain", "repoURL", "repoBranch", "installationId"],
  previews: ["previewsEnabled", "previewsTtl", "previewsDomain"],
  monitoring: ["uptimeEnabled"],
  scaling: ["alwaysOn", "limitMaxConcurrent", "limitRate", "limitBurst"],
};

export function formFromSpec(spec: KusoProject["spec"] | undefined): ProjectSettingsForm {
  const s = spec ?? {};
  return {
    description: s.description ?? "",
    baseDomain: s.baseDomain ?? "",
    repoURL: s.defaultRepo?.url ?? "",
    repoBranch: s.defaultRepo?.defaultBranch ?? "",
    installationId: s.github?.installationId ?? 0,
    previewsEnabled: !!s.previews?.enabled,
    previewsTtl: String(s.previews?.ttlDays ?? 7),
    previewsDomain: s.previews?.baseDomain ?? "",
    uptimeEnabled: s.uptime?.disabled !== true,
    alwaysOn: !!s.alwaysOn,
    limitMaxConcurrent: String(s.requestLimits?.maxConcurrent || ""),
    limitRate: String(s.requestLimits?.ratePerSecond || ""),
    limitBurst: String(s.requestLimits?.burst || ""),
  };
}

export function sectionDirty(
  section: ProjectSettingsSection,
  form: ProjectSettingsForm,
  base: ProjectSettingsForm,
): boolean {
  return PROJECT_SETTINGS_FIELDS[section].some((k) => form[k] !== base[k]);
}

// Empty or non-numeric falls back to the 7-day default; anything else is
// clamped to the 1..30 range the input advertises.
export function clampPreviewTtl(raw: string): number {
  const n = parseInt(raw, 10);
  if (Number.isNaN(n)) return 7;
  return Math.min(30, Math.max(1, n));
}

export type SectionPatch = { ok: true; body: UpdateProjectBody } | { ok: false; error: string };

const LIMIT_LABEL = {
  maxConcurrent: "Max concurrent requests",
  ratePerSecond: "Requests per second",
  burst: "Burst",
} as const;

export function sectionPatch(
  section: ProjectSettingsSection,
  form: ProjectSettingsForm,
  base: ProjectSettingsForm,
): SectionPatch {
  const changed = (k: keyof ProjectSettingsForm) => form[k] !== base[k];
  const body: UpdateProjectBody = {};
  switch (section) {
    case "general":
      if (changed("description")) body.description = form.description.trim();
      if (changed("baseDomain")) body.baseDomain = form.baseDomain.trim();
      if (changed("repoURL") || changed("repoBranch")) {
        // Services without their own repo inherit this; emptying the URL
        // clears it (the server ignores an empty defaultRepo).
        if (form.repoURL.trim()) {
          body.defaultRepo = { url: form.repoURL.trim(), defaultBranch: form.repoBranch.trim() || undefined };
        } else if (base.repoURL) {
          body.clearDefaultRepo = true;
        }
      }
      if (changed("installationId")) body.github = { installationId: form.installationId };
      break;
    case "previews":
      body.previews = {
        enabled: form.previewsEnabled,
        ttlDays: clampPreviewTtl(form.previewsTtl),
        ...(changed("previewsDomain") ? { baseDomain: form.previewsDomain.trim() } : {}),
      };
      break;
    case "monitoring":
      body.uptime = { disabled: !form.uptimeEnabled };
      break;
    case "scaling": {
      if (changed("alwaysOn")) body.alwaysOn = form.alwaysOn;
      if (changed("limitMaxConcurrent") || changed("limitRate") || changed("limitBurst")) {
        // Blank = 0 = the platform default; -1 = no limit (not for burst).
        const raw = { maxConcurrent: form.limitMaxConcurrent, ratePerSecond: form.limitRate, burst: form.limitBurst };
        const limits: Record<string, number> = {};
        for (const [key, v] of Object.entries(raw) as [keyof typeof raw, string][]) {
          const n = v.trim() === "" ? 0 : Number(v);
          if (!Number.isInteger(n) || n < (key === "burst" ? 0 : -1) || n > 1_000_000) {
            return {
              ok: false,
              error: `${LIMIT_LABEL[key]} must be a whole number${key === "burst" ? "" : ", or -1 for no limit"}.`,
            };
          }
          limits[key] = n;
        }
        body.requestLimits = limits;
      }
      break;
    }
  }
  return { ok: true, body };
}
