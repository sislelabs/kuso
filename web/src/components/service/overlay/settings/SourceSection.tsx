"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { Github, ExternalLink } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { useCan, Perms } from "@/features/auth";
import { api } from "@/lib/api-client";
import { Section, Row, type SectionProps } from "./_primitives";

export function SourceSection({
  state,
  setState,
  project,
  service,
}: SectionProps & { project: string; service: string }) {
  // Pull installations so the user can re-point to a repo behind a
  // different GH App install. Best-effort: we don't gate the rest of
  // the section on this query landing.
  const installs = useQuery({
    queryKey: ["github", "installations"],
    queryFn: () =>
      api<{ id: number; accountLogin: string; repositories: { fullName: string }[] }[]>(
        "/api/github/installations",
      ),
    staleTime: 60_000,
  });
  const repoDisplay = state.repoURL.replace(/^https?:\/\/(www\.)?/, "");

  return (
    <Section id="source" title="Source" icon={Github}>
      <Row
        label="display name"
        hint="visual label only · canvas / header"
        control={
          <Input
            value={state.displayName}
            onChange={(e) => setState((s) => ({ ...s, displayName: e.target.value }))}
            placeholder={service}
            className="h-7 w-full text-[12px]"
            maxLength={60}
            spellCheck={false}
          />
        }
      />
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
      <RenameRow project={project} service={service} />
    </Section>
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
  const canWrite = useCan(Perms.ServicesWrite);
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
      toast.error(e instanceof Error ? e.message : "Rename failed");
    } finally {
      setPending(false);
    }
  };

  return (
    <Row
      label="url slug"
      hint={revealed ? "destructive — clones-then-deletes kube resources" : ""}
      control={
        !revealed ? (
          <button
            type="button"
            onClick={() => setRevealed(true)}
            className="font-mono text-[11px] text-[var(--text-tertiary)] underline hover:text-[var(--text-secondary)]"
          >
            change URL slug…
          </button>
        ) : (
        <div className="flex w-full items-center gap-1.5">
          <Input value={service} disabled className="h-7 flex-1 font-mono text-[12px]" />
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
              Rename
            </Button>
          )}
          <ConfirmDialog
            open={open}
            title="Rename service?"
            destructive
            confirmLabel={pending ? "Renaming…" : "Rename"}
            pending={pending}
            body={
              <div className="space-y-3">
                <p className="text-[12px] text-[var(--text-secondary)]">
                  Renaming clones the service under a new name, then deletes the old
                  one. Production traffic returns 503 for the seconds in between, and
                  any service referencing this one via{" "}
                  <span className="font-mono text-[var(--text-tertiary)]">
                    {"${{...}}"}
                  </span>{" "}
                  needs to redeploy to pick up the new DNS.
                </p>
                <Input
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  placeholder="new-service-name"
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
        </div>
        )
      }
    />
  );
}
