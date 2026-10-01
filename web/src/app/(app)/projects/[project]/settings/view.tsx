"use client";

import { useState, useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useRouteParams } from "@/lib/dynamic-params";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useProject,
  useUpdateProject,
  useDeleteProject,
  projectQueryKey,
  getProjectNotificationMute,
  muteProjectNotifications,
  unmuteProjectNotifications,
} from "@/features/projects";
import { SharedSecretsCard } from "@/components/project/SharedSecretsCard";
import { RegistryCredentialsCard } from "@/components/project/RegistryCredentialsCard";
import { ConfigTab } from "@/components/project/ConfigTab";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { friendlyApiError } from "@/features/projects/names";
import { ProjectAccessPanel } from "@/components/project/ProjectAccessPanel";
import { useCan, useProjectRole, Perms } from "@/features/auth/hooks";
import { toast } from "sonner";
import { Trash2, Save, Settings as SettingsIcon, AlertTriangle, Users as UsersIcon } from "lucide-react";

// Empty or non-numeric input falls back to the 7-day default; anything
// else is clamped to the 1..30 range the input advertises.
function clampPreviewTtl(raw: string): number {
  const n = parseInt(raw, 10);
  if (Number.isNaN(n)) return 7;
  return Math.min(30, Math.max(1, n));
}

// Project settings — flat layout, sections separated by horizontal
// rules + small uppercase headers. Mirrors the polish of /settings
// instead of stacking Card components which created visual noise.
export function ProjectSettingsView() {
  const params = useRouteParams<{ project: string }>(["project"]);
  const router = useRouter();
  const projectName = params.project ?? "";
  const qc = useQueryClient();
  const project = useProject(projectName);
  const update = useUpdateProject(projectName);
  const del = useDeleteProject();
  const isAdmin = useCan(Perms.SettingsAdmin);
  // Delete requires project-ADMIN server-side (the single most
  // destructive op); showing the button to editors just hands them a
  // confirm-text ritual that ends in a 403.
  const isProjectAdmin = useProjectRole(projectName) === "admin";

  const [description, setDescription] = useState("");
  const [baseDomain, setBaseDomain] = useState("");
  const [repoURL, setRepoURL] = useState("");
  const [repoBranch, setRepoBranch] = useState("");
  const [previewsEnabled, setPreviewsEnabled] = useState(false);
  // Kept as the raw input string so the field can be cleared while
  // typing; clamped on blur and on save.
  const [previewsTtl, setPreviewsTtl] = useState("7");
  const [alwaysOn, setAlwaysOn] = useState(false);
  // dirty pins the form once the user edits it: the describe payload
  // carries live env status, so it refetches (and would re-seed every
  // field) on focus and after unrelated mutations.
  const [dirty, setDirty] = useState(false);
  const [confirmDomain, setConfirmDomain] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState("");
  const [purgeData, setPurgeData] = useState(false);
  // Notification mute is NOT part of the project spec (it lives in the
  // control-plane DB and takes effect immediately), so the toggle acts
  // on change instead of waiting for Save. null = still loading.
  const [muted, setMuted] = useState<boolean | null>(null);
  const [muteBusy, setMuteBusy] = useState(false);
  const [muteLoadError, setMuteLoadError] = useState<string | null>(null);

  useEffect(() => {
    if (!projectName) return;
    setMuteLoadError(null);
    getProjectNotificationMute(projectName)
      .then((m) => setMuted(m.muted))
      .catch((e: unknown) => setMuteLoadError(e instanceof Error ? e.message : "request failed"));
  }, [projectName]);

  const spec = project.data?.project?.spec;
  const specKey = JSON.stringify(spec ?? null);
  useEffect(() => {
    if (spec && !dirty) {
      const s = spec;
      setDescription(s.description ?? "");
      setBaseDomain(s.baseDomain ?? "");
      setRepoURL(s.defaultRepo?.url ?? "");
      setRepoBranch(s.defaultRepo?.defaultBranch ?? "");
      setPreviewsEnabled(!!s.previews?.enabled);
      setPreviewsTtl(String(s.previews?.ttlDays ?? 7));
      setAlwaysOn(!!s.alwaysOn);
    }
    // specKey stands in for spec: re-seed on content change, not on
    // every refetch's new object identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [specKey, dirty]);

  if (project.isPending) {
    return (
      <div className="mx-auto max-w-3xl p-6 lg:p-8">
        <Skeleton className="mb-4 h-8 w-48" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (project.isError) {
    return (
      <div className="mx-auto max-w-3xl p-6 lg:p-8">
        <p className="rounded-md border border-[var(--error)]/30 bg-[var(--error-subtle)] p-4 text-sm text-[var(--error)]">
          {project.error?.message}
        </p>
      </div>
    );
  }

  const storedBaseDomain = spec?.baseDomain ?? "";
  const storedRepoURL = spec?.defaultRepo?.url ?? "";

  const save = async () => {
    try {
      await update.mutateAsync({
        // "" clears; the server treats an omitted/null key as "leave alone".
        description: description.trim(),
        baseDomain: baseDomain.trim(),
        // Default repo: services with no spec.repo inherit this. An
        // emptied field clears it; the server ignores an empty defaultRepo.
        ...(repoURL.trim()
          ? { defaultRepo: { url: repoURL.trim(), defaultBranch: repoBranch.trim() || undefined } }
          : storedRepoURL
            ? { clearDefaultRepo: true }
            : {}),
        previews: { enabled: previewsEnabled, ttlDays: clampPreviewTtl(previewsTtl) },
        alwaysOn,
      });
      await qc.invalidateQueries({ queryKey: projectQueryKey(projectName) });
      setDirty(false);
      toast.success("Saved");
    } catch (e) {
      toast.error(friendlyApiError(e, "Failed to save"));
    }
  };

  const onSave = () => {
    // A base-domain change re-hosts every service that uses the default
    // host and mints new certificates, so it gets its own confirm.
    if (baseDomain.trim() !== storedBaseDomain) {
      setConfirmDomain(true);
      return;
    }
    void save();
  };

  const onDelete = async () => {
    if (confirmDelete !== projectName) {
      toast.error("Type the project name to confirm");
      return;
    }
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
  };

  return (
    <div className="mx-auto max-w-3xl space-y-10 p-6 lg:p-8">
      <header className="flex items-start gap-3">
        <SettingsIcon className="mt-1 h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">Project settings</h1>
          <p className="mt-1 font-mono text-[12px] text-[var(--text-secondary)]">{projectName}</p>
        </div>
      </header>

      {/* General */}
      <section className="space-y-4">
        <header>
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            general
          </h2>
        </header>
        <div className="space-y-4 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-4">
          <div className="space-y-1.5">
            <Label htmlFor="description">Description</Label>
            <Input
              id="description"
              value={description}
              onChange={(e) => { setDescription(e.target.value); setDirty(true); }}
              placeholder="Short human-readable summary"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="baseDomain">Base domain</Label>
            <Input
              id="baseDomain"
              value={baseDomain}
              onChange={(e) => { setBaseDomain(e.target.value); setDirty(true); }}
              placeholder="myproject.example.com"
              className="font-mono"
            />
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
              Services in this project default to{" "}
              <code className="rounded bg-[var(--bg-tertiary)] px-1">
                &lt;service&gt;.{baseDomain || "<base>"}
              </code>
              ; a service named {projectName} gets the bare domain.
            </p>
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
              DNS: add an A record for{" "}
              <code className="rounded bg-[var(--bg-tertiary)] px-1">*.{baseDomain || "<base>"}</code>{" "}
              (and one for the bare domain) pointing at your cluster&apos;s public IP.
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="repoURL">Default repository</Label>
            <div className="flex gap-2">
              <Input
                id="repoURL"
                value={repoURL}
                onChange={(e) => { setRepoURL(e.target.value); setDirty(true); }}
                placeholder="https://github.com/org/repo"
                className="font-mono flex-1"
              />
              <Input
                id="repoBranch"
                value={repoBranch}
                onChange={(e) => { setRepoBranch(e.target.value); setDirty(true); }}
                placeholder="main"
                className="font-mono w-32"
                aria-label="Default branch"
              />
            </div>
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
              Services without their own repository inherit this one. Pushes and PRs on it
              trigger builds and preview envs. Leave blank if every service sets its own repo.
            </p>
          </div>
        </div>
      </section>

      {/* Preview environments */}
      <section className="space-y-4">
        <header>
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            preview environments
          </h2>
        </header>
        <div className="space-y-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-4">
          {/* Recommendation banner — copy explicitly calls out the use
              cases this feature was built for, so users don't enable it
              for a single-service Express backend and then wonder why
              their cluster is full of 1-pod previews. */}
          <div className="rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3 text-[11px] text-[var(--text-secondary)]">
            <p className="font-medium text-[var(--text-primary)]">
              Recommended for monorepos and full-stack apps
            </p>
            <p className="mt-1">
              Per-PR previews shine when a single PR can change the frontend, the API, and a
              shared DB schema — kuso spins up a complete environment on every PR so reviewers
              can click through the full app, not just CI logs. Examples:
              Next.js fullstack apps, monorepos with API + worker + web, Rails+Inertia. For a
              single-service backend, &ldquo;+ New environment&rdquo; in the env switcher
              (mirror once, share the URL) is usually a better fit than spinning up an env on
              every PR.
            </p>
          </div>
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={previewsEnabled}
              onChange={(e) => { setPreviewsEnabled(e.target.checked); setDirty(true); }}
              className="mt-0.5 h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
            />
            <span className="flex-1">
              <span className="text-[13px] font-medium">Spawn a preview env on every PR</span>
              <span className="mt-0.5 block text-[11px] text-[var(--text-tertiary)]">
                Requires the GitHub App installed on the repo and the default repository
                set above. Each preview clones every service and runs them at{" "}
                <span className="font-mono">
                  &lt;svc&gt;-pr-&lt;N&gt;.{baseDomain || "<base>"}
                </span>
                . The env tears down automatically when the PR merges or closes; the auto-
                expire below is the safety net for missed close webhooks.
              </span>
            </span>
          </label>
          {previewsEnabled && (
            <div className="space-y-1.5 pl-6">
              <Label htmlFor="previewsTtl">Auto-expire after (days)</Label>
              <Input
                id="previewsTtl"
                type="number"
                value={previewsTtl}
                min={1}
                max={30}
                onChange={(e) => { setPreviewsTtl(e.target.value); setDirty(true); }}
                onBlur={(e) => setPreviewsTtl(String(clampPreviewTtl(e.target.value)))}
                className="w-32 font-mono"
              />
              <p className="text-[10px] text-[var(--text-tertiary)]">
                Each preview gets its own Postgres and Redis addons (Postgres starts as a
                copy of production data), so reviewers never write to production. This is on
                by default; a server started with{" "}
                <code className="font-mono">KUSO_PREVIEW_DB_DISABLED=true</code> skips the
                clones, and previews then use the production addons.
              </p>
            </div>
          )}
        </div>
      </section>

      {/* Notifications */}
      <section className="space-y-4">
        <header>
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            notifications
          </h2>
        </header>
        <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-4">
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={muted === true}
              disabled={muted === null || muteBusy}
              onChange={async (e) => {
                const next = e.target.checked;
                setMuteBusy(true);
                try {
                  if (next) {
                    await muteProjectNotifications(projectName);
                  } else {
                    await unmuteProjectNotifications(projectName);
                  }
                  setMuted(next);
                  void qc.invalidateQueries({ queryKey: ["admin", "notifications", "muted-projects"] });
                  toast.success(next ? "Notifications muted" : "Notifications unmuted");
                } catch (err) {
                  toast.error(err instanceof Error ? err.message : "Failed to update mute");
                } finally {
                  setMuteBusy(false);
                }
              }}
              className="mt-0.5 h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
            />
            <span className="flex-1">
              <span className="text-[13px] font-medium">Mute external notifications</span>
              <span className="mt-0.5 block text-[11px] text-[var(--text-tertiary)]">
                Stops this project&rsquo;s events (builds, deploys, crashes, info/warn alerts)
                from reaching Discord, Slack, webhooks, email, Telegram, and Pushover.
                Error-severity alerts still page through — a mute silences chatter, not
                &ldquo;service down&rdquo;. The in-app bell feed keeps recording everything,
                so nothing is lost from the audit trail. Applies immediately — no Save needed.
              </span>
            </span>
          </label>
          {muteLoadError && (
            <p role="alert" className="mt-2 text-[11px] text-[var(--error)]">
              Couldn&apos;t load the current mute state: {muteLoadError}
            </p>
          )}
        </div>
      </section>

      {/* Scaling */}
      <section className="space-y-4">
        <header>
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            scaling
          </h2>
        </header>
        <div className="space-y-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-4">
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={alwaysOn}
              onChange={(e) => { setAlwaysOn(e.target.checked); setDirty(true); }}
              className="mt-0.5 h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
            />
            <span className="flex-1">
              <span className="text-[13px] font-medium">Always-on services (disable scale-to-zero)</span>
              <span className="mt-0.5 block text-[11px] text-[var(--text-tertiary)]">
                Overrides every service&apos;s own sleep setting. With this on, services in
                this project never scale below their minimum replica count, however long
                they sit idle. Useful for low-traffic but cold-start-
                sensitive workloads.
              </span>
            </span>
          </label>
        </div>
      </section>

      {/* Save */}
      <ConfirmDialog
        open={confirmDomain}
        title="Change the base domain?"
        body={
          <>
            Every service on a default host moves from{" "}
            <span className="font-mono">*.{storedBaseDomain || "the instance domain"}</span> to{" "}
            <span className="font-mono">*.{baseDomain.trim() || "the instance domain"}</span>, and new
            TLS certificates are requested. The old URLs stop working once this applies. Make sure
            DNS for the new domain already points at the cluster.
          </>
        }
        confirmLabel="Change domain"
        pending={update.isPending}
        onConfirm={() => {
          setConfirmDomain(false);
          void save();
        }}
        onCancel={() => setConfirmDomain(false)}
      />
      <div className="flex justify-end">
        <Button onClick={onSave} disabled={update.isPending}>
          <Save className="h-4 w-4" />
          {update.isPending ? "Saving…" : "Save changes"}
        </Button>
      </div>

      {/* Project secrets — flat now, no Card wrapper */}
      <SharedSecretsCard project={projectName} />

      <RegistryCredentialsCard project={projectName} />

      {/* Access — who can see/act on this project (admin-only). */}
      {isAdmin && (
        <section className="space-y-3">
          <header className="flex items-center gap-2">
            <UsersIcon className="h-4 w-4 text-[var(--text-tertiary)]" />
            <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              access
            </h2>
          </header>
          <ProjectAccessPanel project={projectName} />
        </section>
      )}

      {/* Config as code — kuso.yaml export / dry-run / apply */}
      <ConfigTab project={projectName} />

      {/* Danger zone — project-admin only, mirroring the server gate */}
      {isProjectAdmin && (
      <section className="space-y-3">
        <header className="flex items-center gap-2">
          <AlertTriangle className="h-4 w-4 text-[var(--error)]" />
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--error)]">
            danger zone
          </h2>
        </header>
        <div className="space-y-3 rounded-md border border-[var(--error)]/30 bg-[var(--error-subtle)] p-4">
          <ul className="space-y-0.5 text-[12px] text-[var(--text-secondary)]">
            <li>Deleted: services, environments, addons, builds, secrets.</li>
            <li>
              {purgeData ? "Also deleted" : "Kept"}: addon data and database credentials
              {purgeData ? " — cannot be undone." : " (reusable or removable later)."}
            </li>
          </ul>
          <label className="flex items-center gap-2 text-[12px] text-[var(--text-secondary)]">
            <input
              type="checkbox"
              checked={purgeData}
              onChange={(e) => setPurgeData(e.target.checked)}
              className="h-3.5 w-3.5"
            />
            Also delete data
          </label>
          <div className="space-y-1.5">
            <Label htmlFor="confirmDelete" className="text-[12px]">
              Type{" "}
              <span className="font-mono text-[var(--text-primary)]">{projectName}</span> to
              confirm
            </Label>
            <Input
              id="confirmDelete"
              value={confirmDelete}
              onChange={(e) => setConfirmDelete(e.target.value)}
              className="font-mono"
              spellCheck={false}
              autoComplete="off"
            />
          </div>
          <Button
            variant="destructive"
            size="sm"
            onClick={onDelete}
            disabled={del.isPending || confirmDelete !== projectName}
          >
            <Trash2 className="h-4 w-4" />
            {del.isPending ? "Deleting…" : "Delete project"}
          </Button>
        </div>
      </section>
      )}
    </div>
  );
}
