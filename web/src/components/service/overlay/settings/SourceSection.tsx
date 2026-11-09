"use client";

import { smoothScroll } from "@/lib/reduced-motion";
import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { Github, ExternalLink, Eye, EyeOff, Package } from "lucide-react";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { useCanOnProject, Perms } from "@/features/auth";
import { useProject } from "@/features/projects";
import { api } from "@/lib/api-client";
import { cn } from "@/lib/utils";
import { Section, Field, FieldGrid, Code, inputClass, monoInputClass, textareaClass, type SectionProps } from "./_primitives";
import { DeployHookRow } from "./DeployHookRow";

export function SourceSection({
  state,
  setState,
  project,
  service,
}: SectionProps & { project: string; service: string }) {
  // Pull installations so the user can re-point to a repo behind a
  // different GH App install. Best-effort: we don't gate the rest of
  // the section on this query landing. /api/github/installations is
  // admin-only (it enumerates every connected repo), so a non-admin
  // editor gets a 403 here — the auto-resolution below already falls
  // back to null, and the user can still type the repo URL by hand.
  // retry:false so react-query doesn't hammer the 403.
  const installs = useQuery({
    queryKey: ["github", "installations"],
    queryFn: () =>
      api<{ id: number; accountLogin: string; repositories: { fullName: string }[] }[]>(
        "/api/github/installations",
      ),
    staleTime: 60_000,
    retry: false,
  });
  // An empty branch deploys the project's default; show that branch as
  // the field's value rather than a grey placeholder.
  const proj = useProject(project);
  const inheritedBranch = proj.data?.project.spec.defaultRepo?.defaultBranch || "main";
  const repoDisplay = state.repoURL.replace(/^https?:\/\/(www\.)?/, "");
  // Only treat the repo URL as a clickable link when it parses as an
  // http(s) URL. A stored value like `javascript:alert(1)` would
  // otherwise render into an <a href> and execute on click; render it
  // as inert text instead. new URL() throws on garbage, so the try
  // guards the parse.
  const repoIsHttp = useMemo(() => {
    const raw = state.repoURL.trim();
    if (!raw) return false;
    try {
      const proto = new URL(raw).protocol;
      return proto === "http:" || proto === "https:";
    } catch {
      return false;
    }
  }, [state.repoURL]);
  // Parse owner/repo so we can preview the auto-resolution + run a
  // server-side access probe. Mirrors the server's parser; kept
  // local because the regex is one line.
  const parsed = useMemo(() => {
    const m = state.repoURL.match(/github\.com[:/]+([^/]+)\/([^/.]+)/i);
    return m ? { owner: m[1], repo: m[2] } : null;
  }, [state.repoURL]);
  // Effective provider for UX purposes. The SERVER does the
  // authoritative github-vs-gitlab detection; this is only used to
  // decide whether to surface the GitLab token field. An explicit
  // state.repoProvider override wins; otherwise we sniff the URL host
  // (host contains "gitlab" → gitlab, else github), mirroring the
  // server's inference. Garbage URLs fall back to github.
  const effectiveProvider = useMemo<"github" | "gitlab">(() => {
    if (state.repoProvider === "gitlab") return "gitlab";
    if (state.repoProvider === "github") return "github";
    const raw = state.repoURL.trim();
    if (!raw) return "github";
    let host = "";
    try {
      host = new URL(raw).host.toLowerCase();
    } catch {
      host = raw.toLowerCase();
    }
    return host.includes("gitlab") ? "gitlab" : "github";
  }, [state.repoProvider, state.repoURL]);
  const isGitlab = effectiveProvider === "gitlab";

  const autoResolved = useMemo(() => {
    if (!parsed || !installs.data) return null;
    // Exact full-name match wins.
    for (const inst of installs.data) {
      const want = `${parsed.owner.toLowerCase()}/${parsed.repo.toLowerCase()}`;
      if ((inst.repositories ?? []).some((r) => r.fullName.toLowerCase() === want)) {
        return inst;
      }
    }
    // Owner-match fallback (the App is installed but the repo cache
    // hasn't refreshed yet).
    return (
      installs.data.find(
        (inst) => inst.accountLogin.toLowerCase() === parsed.owner.toLowerCase(),
      ) ?? null
    );
  }, [parsed, installs.data]);

  // A stored token is how a private repo clones when no GitHub App
  // installation reaches it (GitLab, or GitHub with no App).
  const needsToken = !!state.repoURL.trim() && (isGitlab || !autoResolved);

  const providerNote = isGitlab
    ? `GitLab${state.repoProvider === "gitlab" ? "" : " (detected from the URL)"}`
    : autoResolved
      ? <>GitHub, cloned through the <span className="text-[var(--text-primary)]">{autoResolved.accountLogin}</span> GitHub App installation</>
      : "GitHub";

  const displayNameField = (
    <Field label="Display name" hint="Shown on the canvas and in the header. Doesn't change URLs or DNS." htmlFor="svc-display-name">
      <Input
        id="svc-display-name"
        value={state.displayName}
        onChange={(e) => setState((s) => ({ ...s, displayName: e.target.value }))}
        placeholder={service}
        className={inputClass}
        maxLength={60}
        spellCheck={false}
      />
    </Field>
  );

  // An image service has no repo, branch or watch paths: its source is
  // the registry image, which is edited in the Image section (#build).
  if (state.runtime === "image") {
    return (
      <Section id="source" title="Source" icon={Package} description="A pre-built image pulled from a registry. Pushes don't deploy it.">
        {displayNameField}
        <FieldGrid>
          <Field label="Image">
            <Code>{state.imageRepository || "—"}</Code>
          </Field>
          <Field label="Tag">
            <Code>{state.imageTag || "latest"}</Code>
          </Field>
        </FieldGrid>
        <p className="text-xs text-[var(--text-secondary)]">
          <a href="#build" onClick={jumpToImage} className="font-medium text-[var(--link)] underline underline-offset-2">
            Change the image or tag
          </a>{" "}
          in the Image section. Saving a new tag deploys it.
        </p>
      </Section>
    );
  }

  return (
    <Section
      id="source"
      title="Source"
      icon={Github}
      description="Where kuso pulls code from and which pushes trigger a build."
      footerNote="Saving doesn't rebuild. The next push or manual deploy uses the new source."
    >
      {displayNameField}
      <Field label="Repository" hint={<>Full https URL. {providerNote}.</>} htmlFor="svc-repo-url">
        <div className="flex items-center gap-1.5">
          <Input
            id="svc-repo-url"
            value={state.repoURL}
            onChange={(e) => setState((s) => ({ ...s, repoURL: e.target.value }))}
            placeholder="e.g. https://github.com/owner/repo"
            className={cn(monoInputClass, "flex-1")}
            spellCheck={false}
          />
          {state.repoURL && repoIsHttp && (
            <a
              href={state.repoURL}
              target="_blank"
              rel="noreferrer"
              aria-label="Open repository in a new tab"
              title={repoDisplay}
              className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-sm border border-border text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
            >
              <ExternalLink className="h-3.5 w-3.5" />
            </a>
          )}
        </div>
      </Field>
      <FieldGrid>
        <Field
          label="Branch"
          hint={
            state.repoBranch
              ? "Production, and any environment without its own branch, deploys this."
              : "The project's default branch. Production, and any environment without its own branch, deploys this."
          }
          htmlFor="svc-branch"
        >
          <Input
            id="svc-branch"
            value={state.repoBranch || inheritedBranch}
            onChange={(e) => setState((s) => ({ ...s, repoBranch: e.target.value === inheritedBranch && !s.repoBranch ? "" : e.target.value }))}
            className={monoInputClass}
            spellCheck={false}
          />
        </Field>
        <Field label="Root directory" hint="Subdirectory for monorepos. Blank builds from the repo root." htmlFor="svc-path">
          <Input
            id="svc-path"
            value={state.repoPath}
            onChange={(e) => setState((s) => ({ ...s, repoPath: e.target.value }))}
            placeholder="e.g. apps/api"
            className={monoInputClass}
            spellCheck={false}
          />
        </Field>
      </FieldGrid>
      {/* Push-build gate for monorepos. PR previews and manual builds
          ignore it; a push whose file list is unknown builds anyway. */}
      <Field
        label="Watch paths"
        hint="A push only builds when a changed file matches one of these globs, one per line. Blank builds on every push."
        htmlFor="svc-watch-paths"
      >
        <textarea
          id="svc-watch-paths"
          value={state.watchPaths}
          onChange={(e) => setState((s) => ({ ...s, watchPaths: e.target.value }))}
          placeholder={"e.g. apps/web/**\npackages/ui/**"}
          rows={3}
          spellCheck={false}
          className={textareaClass}
        />
      </Field>
      {/* Write-only clone credential for a private repo no GitHub App
          installation reaches. The server stores it in a Secret and never
          returns it; blank on save keeps the existing token. */}
      {needsToken && (
        <Field
          label={isGitlab ? "GitLab access token" : "Access token"}
          hint="Only for private repos. Write-only: leave blank to keep the stored token."
          htmlFor="svc-repo-token"
        >
          <RepoTokenInput
            id="svc-repo-token"
            value={state.repoToken}
            placeholder={isGitlab ? "glpat-…" : "github_pat_…"}
            onChange={(v) => setState((s) => ({ ...s, repoToken: v }))}
          />
        </Field>
      )}
      {state.repoURL.trim() && <DeployHookRow project={project} service={service} />}
      <RenameRow project={project} service={service} />
    </Section>
  );
}

// RepoTokenInput is a masked write-only credential field. The token
// is never read back from the server (it lives in a Secret), so the
// input is always what the user typed this session — starts blank,
// masked as a password by default, with an eye toggle to verify the
// pasted value before saving. Mirrors the masked-input affordance the
// env editor uses (Eye/EyeOff), minus the reveal-fetch since there's
// nothing on the server to reveal.
function RepoTokenInput({
  id,
  value,
  placeholder,
  onChange,
}: {
  id: string;
  value: string;
  placeholder: string;
  onChange: (v: string) => void;
}) {
  const [shown, setShown] = useState(false);
  return (
    <div className="flex w-full items-center gap-1.5">
      <Input
        id={id}
        type={shown ? "text" : "password"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={`${placeholder} (blank keeps the current token)`}
        className={cn(monoInputClass, "flex-1")}
        spellCheck={false}
        autoComplete="off"
        // Hint password managers to leave this alone — it's a clone
        // credential, not a login.
        data-1p-ignore
        data-lpignore="true"
      />
      <button
        type="button"
        onClick={() => setShown((s) => !s)}
        aria-label={shown ? "Hide token" : "Show token"}
        title={shown ? "Hide token" : "Show token"}
        className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-sm border border-border text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
      >
        {shown ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
      </button>
    </div>
  );
}

// RenameRow gates the destructive slug-rename behind a "show" toggle.
// Display-name edits (the common case) live on a separate row above
// and PATCH the CR — fast, no kube churn. The slug rename is a
// clone-then-delete that briefly drops traffic + breaks any service
// referencing the old in-cluster DNS, so it stays one click away
// rather than being a top-level row users can fat-finger.
function RenameRow({ project, service }: { project: string; service: string }) {
  const router = useRouter();
  const qc = useQueryClient();
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const [revealed, setRevealed] = useState(false);
  const [open, setOpen] = useState(false);
  const [newName, setNewName] = useState(service);
  const [pending, setPending] = useState(false);

  const submit = async () => {
    const trimmed = newName.trim();
    if (!trimmed || trimmed === service) {
      toast.error("Pick a different name");
      return;
    }
    if (!/^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$/.test(trimmed)) {
      toast.error("Lowercase letters/digits/dash, ≤32 chars");
      return;
    }
    setPending(true);
    try {
      const { renameService } = await import("@/features/services/api");
      await renameService(project, service, trimmed);
      toast.success(`Renamed to ${trimmed}`);
      // Invalidate every cache the old service name is keyed on.
      // Easiest: blow away the entire project query subtree.
      qc.invalidateQueries({ queryKey: ["projects", project] });
      setOpen(false);
      // Close the overlay by routing back to the project page.
      router.push(`/projects/${encodeURIComponent(project)}`);
    } catch (e) {
      toastError(e, "Rename failed");
    } finally {
      setPending(false);
    }
  };

  return (
    <Field
      label="Service slug"
      hint={
        <>
          The name in URLs and in-cluster DNS (<Code>{service}</Code>). Renaming clones the service and deletes the
          old one, so traffic drops for a few seconds.
        </>
      }
    >
      {!revealed ? (
        <button
          type="button"
          onClick={() => setRevealed(true)}
          className="text-xs text-[var(--text-secondary)] underline underline-offset-2 hover:text-[var(--text-primary)]"
        >
          Change slug…
        </button>
      ) : (
        <div className="flex items-center gap-1.5">
          <Input value={service} disabled aria-label="Current slug" className={cn(monoInputClass, "flex-1")} />
          {canWrite && (
            <Button
              variant="outline"
              size="sm"
              type="button"
              onClick={() => {
                setNewName(service);
                setOpen(true);
              }}
            >
              Rename…
            </Button>
          )}
        </div>
      )}
      <ConfirmDialog
        open={open}
        title="Rename service?"
        destructive
        confirmLabel={pending ? "Renaming…" : "Rename"}
        pending={pending}
        body={
          <div className="space-y-3">
            <p className="text-[13px] text-[var(--text-secondary)]">
              Renaming clones the service under a new name, then deletes the old one. Production returns 503 for
              the seconds in between, and services that reference this one through <Code>{"${{ … }}"}</Code> need a
              redeploy to pick up the new DNS name.
            </p>
            <Input
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              placeholder="new-service-name"
              aria-label="New slug"
              className="font-mono text-sm"
              autoFocus
              spellCheck={false}
            />
          </div>
        }
        onConfirm={submit}
        onCancel={() => {
          if (!pending) setOpen(false);
        }}
      />
    </Field>
  );
}

// jumpToImage scrolls the settings pane to the Image section and focuses
// its tag field, without touching the overlay's route.
export function jumpToImage(e?: { preventDefault: () => void }) {
  e?.preventDefault();
  const el = document.getElementById("build");
  el?.scrollIntoView({ behavior: smoothScroll(), block: "start" });
  (document.getElementById("build-image-tag") as HTMLInputElement | null)?.focus({ preventScroll: true });
}
