"use client";

import { useState, useEffect, useMemo, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import {
  AlertTriangle,
  Bell,
  Download,
  FileCode,
  Gauge,
  GitPullRequest,
  HeartPulse,
  KeyRound,
  Package,
  Settings2,
  Trash2,
  Users as UsersIcon,
} from "lucide-react";
import { toast } from "sonner";
import { useRouteParams } from "@/lib/dynamic-params";
import { useUnsavedEdits } from "@/lib/unsaved-edits";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  useProject,
  useUpdateProject,
  useDeleteProject,
  projectQueryKey,
  getProjectNotificationMute,
  muteProjectNotifications,
  unmuteProjectNotifications,
  exportProjectArchive,
} from "@/features/projects";
import {
  PROJECT_SETTINGS_FIELDS,
  clampPreviewTtl,
  formFromSpec,
  sectionDirty,
  sectionPatch,
  type ProjectSettingsForm,
  type ProjectSettingsSection,
} from "@/features/projects/settingsForm";
import { useInstallations } from "@/features/github";
import { SharedSecretsCard } from "@/components/project/SharedSecretsCard";
import { RegistryCredentialsCard } from "@/components/project/RegistryCredentialsCard";
import { ConfigTab } from "@/components/project/ConfigTab";
import { ProjectAccessPanel } from "@/components/project/ProjectAccessPanel";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { PageHeader } from "@/components/shared/PageHeader";
import { ErrorState } from "@/components/shared/ErrorState";
import {
  Callout,
  Code,
  Field,
  FieldGrid,
  Section,
  SettingsSaveProvider,
  SwitchField,
  inputClass,
  monoInputClass,
  type SectionSaveStatus,
  type SettingsSaveApi,
} from "@/components/service/overlay/settings/_primitives";
import { friendlyApiError } from "@/features/projects/names";
import { useCan, useCanOnProject, useProjectRole, Perms } from "@/features/auth/hooks";

const SAVED_FLASH_MS = 2500;
const SECTION_LABEL: Record<ProjectSettingsSection, string> = {
  general: "General",
  previews: "Preview environments",
  monitoring: "Monitoring",
  scaling: "Scaling",
};
const SECTIONS = Object.keys(PROJECT_SETTINGS_FIELDS) as ProjectSettingsSection[];

// Project settings: the same card language as service settings (label +
// hint above a full-width control, one Save per card, danger zone last).
export function ProjectSettingsView() {
  const params = useRouteParams<{ project: string }>(["project"]);
  const router = useRouter();
  const projectName = params.project ?? "";
  const qc = useQueryClient();
  const project = useProject(projectName);
  const update = useUpdateProject(projectName);
  const del = useDeleteProject();
  const installations = useInstallations();
  const isAdmin = useCan(Perms.SettingsAdmin);
  const canWrite = useCanOnProject(projectName, Perms.ProjectWrite);
  // Delete and archive export need project-ADMIN server-side; showing them
  // to editors only hands them a ritual that ends in a 403.
  const isProjectAdmin = useProjectRole(projectName) === "admin";

  const spec = project.data?.project?.spec;
  const specKey = JSON.stringify(spec ?? null);
  const [form, setForm] = useState<ProjectSettingsForm>(() => formFromSpec(undefined));
  // What the form was last seeded with; each card diffs against it.
  const [base, setBase] = useState<ProjectSettingsForm>(() => formFromSpec(undefined));
  const [saving, setSaving] = useState<ProjectSettingsSection | null>(null);
  const [errors, setErrors] = useState<Partial<Record<ProjectSettingsSection, string>>>({});
  const [savedAt, setSavedAt] = useState<Partial<Record<ProjectSettingsSection, number>>>({});
  const [confirmDomain, setConfirmDomain] = useState(false);
  const seededOnce = useRef(false);

  const dirtySections = SECTIONS.filter((s) => sectionDirty(s, form, base));
  useUnsavedEdits("project-settings", dirtySections.length > 0);

  // The describe payload carries live env status and refetches often:
  // re-seed only the cards the user isn't editing.
  useEffect(() => {
    if (!spec) return;
    const fresh = formFromSpec(spec);
    if (!seededOnce.current) {
      seededOnce.current = true;
      setForm(fresh);
      setBase(fresh);
      return;
    }
    const editing = new Set(SECTIONS.filter((s) => sectionDirty(s, form, base)));
    const reseed = (prev: ProjectSettingsForm) => {
      const next = { ...prev };
      for (const s of SECTIONS) {
        if (editing.has(s)) continue;
        for (const k of PROJECT_SETTINGS_FIELDS[s]) (next as Record<string, unknown>)[k] = fresh[k];
      }
      return next;
    };
    setForm(reseed);
    setBase(reseed);
    // specKey stands in for spec: re-seed on content change only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [specKey]);

  useEffect(() => {
    if (Object.keys(savedAt).length === 0) return;
    const t = window.setTimeout(() => setSavedAt({}), SAVED_FLASH_MS);
    return () => window.clearTimeout(t);
  }, [savedAt]);

  const set = <K extends keyof ProjectSettingsForm>(key: K, value: ProjectSettingsForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  const saveSection = async (section: ProjectSettingsSection) => {
    const patch = sectionPatch(section, form, base);
    if (!patch.ok) {
      setErrors((e) => ({ ...e, [section]: patch.error }));
      return;
    }
    setSaving(section);
    setErrors((e) => ({ ...e, [section]: undefined }));
    try {
      await update.mutateAsync(patch.body);
      // Adopt this card's values as the new baseline right away so it
      // reads clean even before the refetch lands.
      setBase((b) => {
        const next = { ...b };
        for (const k of PROJECT_SETTINGS_FIELDS[section]) (next as Record<string, unknown>)[k] = form[k];
        return next;
      });
      setSavedAt((s) => ({ ...s, [section]: Date.now() }));
      toast.success(`${SECTION_LABEL[section]} saved`);
      await qc.invalidateQueries({ queryKey: projectQueryKey(projectName) });
    } catch (e) {
      setErrors((x) => ({ ...x, [section]: friendlyApiError(e, "Failed to save") }));
    } finally {
      setSaving(null);
    }
  };

  const saveApi: SettingsSaveApi = useMemo(
    () => ({
      canWrite,
      status: (id: string): SectionSaveStatus | null => {
        if (!(SECTIONS as string[]).includes(id)) return null;
        const s = id as ProjectSettingsSection;
        return {
          dirty: sectionDirty(s, form, base),
          saving: saving === s,
          saved: savedAt[s] !== undefined,
          error: errors[s] ?? null,
        };
      },
      save: (id: string) => {
        const s = id as ProjectSettingsSection;
        // A base-domain change re-hosts every default-host service and
        // mints new certificates, so it gets its own confirm.
        if (s === "general" && form.baseDomain.trim() !== base.baseDomain.trim()) {
          setConfirmDomain(true);
          return;
        }
        void saveSection(s);
      },
      discard: (id: string) => {
        const s = id as ProjectSettingsSection;
        setForm((f) => {
          const next = { ...f };
          for (const k of PROJECT_SETTINGS_FIELDS[s]) (next as Record<string, unknown>)[k] = base[k];
          return next;
        });
        setErrors((e) => ({ ...e, [s]: undefined }));
      },
    }),
    // saveSection closes over form/base, which are already deps.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [canWrite, form, base, saving, savedAt, errors],
  );

  if (project.isPending) {
    return (
      <div className="mx-auto max-w-5xl px-4 pt-6 sm:px-6 sm:pt-8 lg:px-8">
        <Skeleton className="mb-4 h-8 w-48" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (project.isError) {
    return (
      <div className="mx-auto max-w-3xl px-4 pt-6 sm:px-6 sm:pt-8 lg:px-8">
        <ErrorState what="project settings" error={project.error} onRetry={() => void project.refetch()} back={{ href: "/projects", label: "All projects" }} />
      </div>
    );
  }

  const domainShown = form.baseDomain.trim() || "<base>";
  const nav: { id: string; label: string }[] = [
    { id: "general", label: "General" },
    { id: "previews", label: "Preview environments" },
    { id: "notifications", label: "Notifications" },
    { id: "monitoring", label: "Monitoring" },
    { id: "scaling", label: "Scaling" },
    { id: "secrets", label: "Project secrets" },
    { id: "registry", label: "Registry credentials" },
    ...(isAdmin ? [{ id: "access", label: "Access" }] : []),
    { id: "config", label: "Config as code" },
    ...(isProjectAdmin ? [{ id: "archive", label: "Archive" }, { id: "danger", label: "Danger zone" }] : []),
  ];

  return (
    <div className="mx-auto w-full max-w-5xl px-4 pt-6 pb-12 sm:px-6 sm:pt-8 lg:px-8">
      <PageHeader
        breadcrumb={[
          { label: "Projects", href: "/projects" },
          { label: projectName, href: `/projects/${encodeURIComponent(projectName)}` },
          { label: "Settings" },
        ]}
        title="Project settings"
        description="Domains, repository, preview environments, access and the danger zone for this project."
      />

      <div className="flex gap-10">
        <nav aria-label="Project settings sections" className="sticky top-6 hidden h-fit w-44 shrink-0 lg:block">
          <ul className="space-y-0.5 text-[13px]">
            {nav.map((n) => {
              const dirty = (SECTIONS as string[]).includes(n.id) && dirtySections.includes(n.id as ProjectSettingsSection);
              return (
                <li key={n.id}>
                  <a
                    href={`#${n.id}`}
                    className={cn(
                      "flex items-center gap-2 rounded-md px-2 py-1.5 text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]",
                      n.id === "danger" && "text-[var(--error)] hover:text-[var(--error)]",
                    )}
                  >
                    <span className="flex-1 truncate">{n.label}</span>
                    {dirty && (
                      <span aria-label="Unsaved changes" title="Unsaved changes" className="h-1.5 w-1.5 rounded-full bg-[var(--warning)]" />
                    )}
                  </a>
                </li>
              );
            })}
          </ul>
        </nav>

        <div className="min-w-0 flex-1 space-y-6">
          <SettingsSaveProvider value={saveApi}>
            <Section
              id="general"
              title="General"
              icon={Settings2}
              description="What this project is called, where its services are served and which repository they build from."
            >
              <Field label="Description" htmlFor="description">
                <Input
                  id="description"
                  value={form.description}
                  onChange={(e) => set("description", e.target.value)}
                  placeholder="Short human-readable summary"
                  className={inputClass}
                  disabled={!canWrite}
                />
              </Field>
              <Field
                label="Base domain"
                htmlFor="baseDomain"
                hint={
                  <>
                    Services default to <Code>&lt;service&gt;.{domainShown}</Code>; a service named {projectName} gets the bare
                    domain. DNS: an A record for <Code>*.{domainShown}</Code> (and the bare domain) pointing at the cluster&apos;s
                    public IP.
                  </>
                }
              >
                <Input
                  id="baseDomain"
                  value={form.baseDomain}
                  onChange={(e) => set("baseDomain", e.target.value)}
                  placeholder="e.g. myproject.example.com"
                  className={monoInputClass}
                  spellCheck={false}
                  disabled={!canWrite}
                />
              </Field>
              <Field
                label="Default repository and branch"
                htmlFor="repoURL"
                hint="Services without their own repository inherit this one, and pushes to the branch deploy production. Leave blank if every service sets its own repo."
              >
                <div className="flex gap-2">
                  <Input
                    id="repoURL"
                    value={form.repoURL}
                    onChange={(e) => set("repoURL", e.target.value)}
                    placeholder="https://github.com/org/repo"
                    className={cn(monoInputClass, "min-w-0 flex-1")}
                    spellCheck={false}
                    disabled={!canWrite}
                  />
                  <Input
                    id="repoBranch"
                    aria-label="Default branch"
                    value={form.repoBranch}
                    onChange={(e) => set("repoBranch", e.target.value)}
                    placeholder="main"
                    className={cn(monoInputClass, "w-28 shrink-0 sm:w-36")}
                    spellCheck={false}
                    disabled={!canWrite}
                  />
                </div>
              </Field>
              <Field
                label="GitHub App installation"
                htmlFor="installation"
                hint="The installation kuso mints clone tokens from for private repos. None means public repos only, unless a service sets its own."
              >
                <Select
                  id="installation"
                  size="sm"
                  value={form.installationId}
                  onChange={(e) => set("installationId", Number(e.target.value))}
                  disabled={!canWrite}
                >
                  <option value={0}>None</option>
                  {(installations.data ?? []).map((i) => (
                    <option key={i.id} value={i.id}>
                      {i.accountLogin} ({i.accountType.toLowerCase()})
                    </option>
                  ))}
                  {form.installationId > 0 && !(installations.data ?? []).some((i) => i.id === form.installationId) && (
                    <option value={form.installationId}>#{form.installationId} (not visible to kuso)</option>
                  )}
                </Select>
              </Field>
            </Section>

            <Section
              id="previews"
              title="Preview environments"
              icon={GitPullRequest}
              status={base.previewsEnabled ? "On" : "Off"}
              description="A full copy of the project for every pull request, torn down when the PR closes. Best for monorepos and full-stack apps where one PR touches the frontend, the API and the schema."
            >
              <SwitchField
                label="Spawn a preview environment on every PR"
                on={form.previewsEnabled}
                onChange={(v) => set("previewsEnabled", v)}
                disabled={!canWrite}
                hint={
                  <>
                    Needs the GitHub App on the repo and a default repository. Each preview clones every service at{" "}
                    <Code>&lt;svc&gt;-pr-&lt;N&gt;.{form.previewsDomain.trim() || domainShown}</Code>. For a single-service
                    backend, a named environment from the env switcher is usually the better fit.
                  </>
                }
              />
              {form.previewsEnabled && (
                <>
                  <FieldGrid>
                    <Field
                      label="Auto-expire after"
                      htmlFor="previewsTtl"
                      hint="Days, 1–30. The safety net for missed PR-close webhooks."
                    >
                      <Input
                        id="previewsTtl"
                        type="number"
                        min={1}
                        max={30}
                        value={form.previewsTtl}
                        onChange={(e) => set("previewsTtl", e.target.value)}
                        onBlur={(e) => set("previewsTtl", String(clampPreviewTtl(e.target.value)))}
                        className={monoInputClass}
                        disabled={!canWrite}
                      />
                    </Field>
                    <Field
                      label="Preview domain"
                      htmlFor="previewsDomain"
                      hint="Needs a wildcard DNS record. Blank uses the base domain."
                    >
                      <Input
                        id="previewsDomain"
                        value={form.previewsDomain}
                        onChange={(e) => set("previewsDomain", e.target.value)}
                        placeholder={form.baseDomain || "same as base domain"}
                        className={monoInputClass}
                        spellCheck={false}
                        disabled={!canWrite}
                      />
                    </Field>
                  </FieldGrid>
                  <Callout>
                    Each preview gets its own Postgres and Redis (Postgres starts as a copy of production data), so reviewers
                    never write to production. A server started with <Code>KUSO_PREVIEW_DB_DISABLED=true</Code> skips the
                    copies and previews share the production addons.
                  </Callout>
                </>
              )}
            </Section>

            <NotificationsCard project={projectName} canWrite={canWrite} />

            <Section
              id="monitoring"
              title="Monitoring"
              icon={HeartPulse}
              description="Health checks kuso runs against this project's services."
            >
              <SwitchField
                label="Uptime checks"
                hint="Ping every production service once a minute and notify when one stops answering."
                on={form.uptimeEnabled}
                onChange={(v) => set("uptimeEnabled", v)}
                disabled={!canWrite}
              />
            </Section>

            <Section
              id="scaling"
              title="Scaling"
              icon={Gauge}
              description="Defaults that apply to every service in the project. A service can override the ingress limits under its own Networking settings."
            >
              <SwitchField
                label="Always on"
                hint="Disables scale-to-zero for every service, overriding their own sleep settings. Useful for low-traffic but cold-start-sensitive workloads."
                on={form.alwaysOn}
                onChange={(v) => set("alwaysOn", v)}
                disabled={!canWrite}
              />
              <Field
                label="Ingress limits"
                hint="All projects share one ingress. Past these limits a service answers 429 and everything else keeps being served. Counted per ingress replica. Blank uses the platform default (1000 concurrent, no rate limit); -1 removes a limit."
              >
                <FieldGrid cols={3}>
                  <LimitInput id="limitMaxConcurrent" label="Max concurrent" placeholder="1000" min={-1} value={form.limitMaxConcurrent} onChange={(v) => set("limitMaxConcurrent", v)} disabled={!canWrite} />
                  <LimitInput id="limitRate" label="Requests per second" placeholder="No limit" min={-1} value={form.limitRate} onChange={(v) => set("limitRate", v)} disabled={!canWrite} />
                  <LimitInput id="limitBurst" label="Burst" placeholder="Same as rate" min={0} value={form.limitBurst} onChange={(v) => set("limitBurst", v)} disabled={!canWrite} />
                </FieldGrid>
              </Field>
            </Section>

            <Section
              id="secrets"
              title="Project secrets"
              icon={KeyRound}
              description="Set a value once, then pick it from any service's Variables tab under Add → Project secret."
            >
              <SharedSecretsCard project={projectName} />
            </Section>

            <Section
              id="registry"
              title="Registry credentials"
              icon={Package}
              description="Logins for private registries (GHCR, ECR, Docker Hub). Attach one to an image service under its Image settings. Passwords are never shown again."
            >
              <RegistryCredentialsCard project={projectName} />
            </Section>

            {isAdmin && (
              <Section
                id="access"
                title="Access"
                icon={UsersIcon}
                description="Who can see and change this project. Instance admins always can."
              >
                <ProjectAccessPanel project={projectName} />
              </Section>
            )}

            <Section
              id="config"
              title="Config as code"
              icon={FileCode}
              description={
                <>
                  The project&apos;s services, addons and crons as one <Code>kuso.yaml</Code>. Edit, dry-run to preview the
                  diff, then apply. Apply never deletes resources missing from the file; it lists them instead.
                </>
              }
            >
              <ConfigTab project={projectName} />
            </Section>

            {isProjectAdmin && <ArchiveCard project={projectName} />}
            {isProjectAdmin && (
              <DangerCard
                project={projectName}
                pending={del.isPending}
                onDelete={async (purgeData) => {
                  try {
                    const res = await del.mutateAsync({ name: projectName, purgeData });
                    const warnings = res?.warnings ?? [];
                    if (warnings.length > 0) {
                      toast.warning("Project deleted with leftovers", { description: warnings.join("\n") });
                    } else {
                      toast.success("Project deleted");
                    }
                    router.replace("/projects");
                  } catch (e) {
                    toast.error(friendlyApiError(e, "Failed to delete"));
                  }
                }}
              />
            )}
          </SettingsSaveProvider>
        </div>
      </div>

      <ConfirmDialog
        open={confirmDomain}
        title="Change the base domain?"
        body={
          <>
            Every service on a default host moves from{" "}
            <span className="font-mono">*.{base.baseDomain || "the instance domain"}</span> to{" "}
            <span className="font-mono">*.{form.baseDomain.trim() || "the instance domain"}</span>, and new TLS
            certificates are requested. The old URLs stop working once this applies. Make sure DNS for the new domain
            already points at the cluster.
          </>
        }
        confirmLabel="Change domain"
        pending={saving === "general"}
        onConfirm={() => {
          setConfirmDomain(false);
          void saveSection("general");
        }}
        onCancel={() => setConfirmDomain(false)}
      />
    </div>
  );
}

function LimitInput({
  id,
  label,
  placeholder,
  min,
  value,
  onChange,
  disabled,
}: {
  id: string;
  label: string;
  placeholder: string;
  min: number;
  value: string;
  onChange: (v: string) => void;
  disabled: boolean;
}) {
  return (
    <div className="min-w-0 space-y-1.5">
      <label htmlFor={id} className="block text-xs text-[var(--text-secondary)]">
        {label}
      </label>
      <Input
        id={id}
        type="number"
        min={min}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className={monoInputClass}
        disabled={disabled}
      />
    </div>
  );
}

// Mute lives in the control-plane DB, not the project spec, so it applies
// on toggle: this card has no Save footer.
function NotificationsCard({ project, canWrite }: { project: string; canWrite: boolean }) {
  const qc = useQueryClient();
  const [muted, setMuted] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);

  useEffect(() => {
    if (!project) return;
    setLoadError(null);
    getProjectNotificationMute(project)
      .then((m) => setMuted(m.muted))
      .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : "request failed"));
  }, [project]);

  const toggle = async (next: boolean) => {
    setBusy(true);
    try {
      if (next) await muteProjectNotifications(project);
      else await unmuteProjectNotifications(project);
      setMuted(next);
      void qc.invalidateQueries({ queryKey: ["admin", "notifications", "muted-projects"] });
      toast.success(next ? "Notifications muted" : "Notifications unmuted");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to update mute");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section
      id="notifications"
      title="Notifications"
      icon={Bell}
      status={muted === null ? undefined : muted ? "Muted" : "On"}
      description="Applies immediately, no Save needed."
    >
      <SwitchField
        label="Mute external notifications"
        hint="Stops builds, deploys, crashes and info/warn alerts from reaching Discord, Slack, webhooks, email, Telegram and Pushover. Error-severity alerts still page through, and the in-app feed keeps recording everything."
        on={muted === true}
        onChange={(v) => void toggle(v)}
        disabled={!canWrite || muted === null || busy}
      />
      {loadError && (
        <p role="alert" className="text-xs text-[var(--error)]">
          Couldn&apos;t load the current mute state: {loadError}
        </p>
      )}
    </Section>
  );
}

function ArchiveCard({ project }: { project: string }) {
  const [exporting, setExporting] = useState(false);
  const [confirm, setConfirm] = useState(false);
  return (
    <Section
      id="archive"
      title="Archive"
      icon={Download}
      description="A tarball of this project's services, environments, addons and secret values, to move it to another kuso instance (import it from New project). Addon data isn't included; use addon backups for that."
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-xs text-[var(--text-secondary)]">
          The file holds every secret in plain text. Store it like a credential.
        </p>
        <Button variant="outline" size="sm" onClick={() => setConfirm(true)} disabled={exporting}>
          <Download className="h-4 w-4" />
          {exporting ? "Exporting…" : "Export archive"}
        </Button>
      </div>
      <ConfirmDialog
        open={confirm}
        title="Export with secrets?"
        body={
          <>
            The archive contains every secret value in <span className="font-mono">{project}</span> in plain text. The
            export is recorded in the audit log.
          </>
        }
        destructive={false}
        confirmLabel="Download"
        pending={exporting}
        onConfirm={async () => {
          setExporting(true);
          try {
            await exportProjectArchive(project);
            setConfirm(false);
          } catch (e) {
            toast.error(friendlyApiError(e, "Export failed"));
          } finally {
            setExporting(false);
          }
        }}
        onCancel={() => setConfirm(false)}
      />
    </Section>
  );
}

function DangerCard({
  project,
  pending,
  onDelete,
}: {
  project: string;
  pending: boolean;
  onDelete: (purgeData: boolean) => Promise<void>;
}) {
  const [typed, setTyped] = useState("");
  const [purgeData, setPurgeData] = useState(false);
  const purgeId = "purge-data";
  return (
    <Section
      id="danger"
      title="Danger zone"
      icon={AlertTriangle}
      tone="danger"
      description="Deleting a project removes its services, environments, addons, builds and secrets. It can't be undone."
    >
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 space-y-0.5">
          <div id={`${purgeId}-label`} className="text-[13px] font-medium text-[var(--text-primary)]">
            Also delete addon data
          </div>
          <p className="text-xs leading-relaxed text-[var(--text-secondary)]">
            {purgeData
              ? "Database volumes and credentials are deleted too. This is permanent."
              : "Database volumes and credentials are kept so they can be reused or removed later."}
          </p>
        </div>
        <Switch on={purgeData} onChange={setPurgeData} labelledBy={`${purgeId}-label`} />
      </div>
      <Field
        label={
          <>
            Type <span className="font-mono">{project}</span> to confirm
          </>
        }
        htmlFor="confirmDelete"
      >
        <Input
          id="confirmDelete"
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          className={monoInputClass}
          spellCheck={false}
          autoComplete="off"
        />
      </Field>
      <div className="flex justify-end">
        <Button variant="destructive" size="sm" onClick={() => void onDelete(purgeData)} disabled={pending || typed !== project}>
          <Trash2 className="h-4 w-4" />
          {pending ? "Deleting…" : purgeData ? "Delete project and data" : "Delete project"}
        </Button>
      </div>
    </Section>
  );
}
