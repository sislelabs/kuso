"use client";

import { useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Github, ArrowRight, Check, Plus } from "lucide-react";
import { toast } from "sonner";
import {
  useInstallURL,
  useInstallations,
  useDetectRuntime,
  type GithubRepo,
  type DetectRuntimeResponse,
} from "@/features/github";
import { useRouteParams } from "@/lib/dynamic-params";
import { api, ApiError } from "@/lib/api-client";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";

// AddServiceView is the per-project add-service flow. Pick a repo,
// kuso detects the runtime + port, you confirm name + path, click
// add. The wizard intentionally only adds ONE service at a time —
// monorepo support stays via repeated runs of this flow rather than
// the legacy multi-row UI which conflated project + services.
export function AddServiceView() {
  const router = useRouter();
  const params = useRouteParams<{ project: string }>(["project"]);
  const project = params.project ?? "";

  const installURL = useInstallURL();
  const installs = useInstallations();
  const detect = useDetectRuntime();

  const [picked, setPicked] = useState<{ installationId: number; repo: GithubRepo } | null>(null);
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [runtime, setRuntime] = useState<string>("dockerfile");
  const [port, setPort] = useState<string>("");
  const [reason, setReason] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [repoQuery, setRepoQuery] = useState("");

  const allRepos = useMemo(() => {
    return (installs.data ?? []).flatMap((inst) =>
      inst.repositories.map((r) => ({ installationId: inst.id, repo: r, owner: inst.accountLogin }))
    );
  }, [installs.data]);

  const filteredRepos = useMemo(() => {
    const q = repoQuery.trim().toLowerCase();
    if (!q) return allRepos;
    return allRepos.filter(({ repo }) => repo.fullName.toLowerCase().includes(q));
  }, [allRepos, repoQuery]);

  // Prefill name from repo + run detect on first pick.
  useEffect(() => {
    if (!picked) return;
    const repoName = picked.repo.fullName.split("/")[1] ?? "service";
    if (!name) setName(repoName);

    const [owner, repoOnly] = picked.repo.fullName.split("/");
    detect
      .mutateAsync({
        installationId: picked.installationId,
        owner: owner ?? "",
        repo: repoOnly ?? "",
        branch: picked.repo.defaultBranch,
        path: "",
      })
      .then((res: DetectRuntimeResponse) => {
        setRuntime(res.runtime ?? "dockerfile");
        if (res.port) setPort(String(res.port));
        setReason(res.reason ?? null);
      })
      .catch(() => {
        /* leave defaults */
      });
  }, [picked]);

  const onAdd = async () => {
    if (!picked) {
      toast.error("Pick a repo first");
      return;
    }
    if (!name.trim()) {
      toast.error("Service name required");
      return;
    }
    setSubmitting(true);
    try {
      await api(`/api/projects/${encodeURIComponent(project)}/services`, {
        method: "POST",
        body: {
          name: name.trim(),
          repo: {
            url: `https://github.com/${picked.repo.fullName}`,
            defaultBranch: picked.repo.defaultBranch,
            ...(path.trim() ? { path: path.trim() } : {}),
          },
          runtime,
          ...(port ? { port: parseInt(port, 10) } : {}),
          github: { installationId: picked.installationId },
        },
      });
      toast.success(`Service ${name} added`);
      router.replace(`/projects/${encodeURIComponent(project)}`);
    } catch (e) {
      if (e instanceof ApiError) {
        toast.error(e.message);
      } else {
        toast.error(e instanceof Error ? e.message : "Failed to add service");
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="mx-auto max-w-2xl p-6 lg:p-8 space-y-4">
      <header>
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Add service</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          Adding to <span className="font-mono text-[var(--text-primary)]">{project}</span>. Pick a
          GitHub repo; kuso detects the runtime and port.
        </p>
      </header>

      {/* Repo picker */}
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <div className="border-b border-[var(--border-subtle)] px-4 py-2.5">
          <h2 className="text-sm font-semibold tracking-tight">Repository</h2>
        </div>
        <div className="px-4 py-3">
          {installURL.isPending || installs.isPending ? (
            <Skeleton className="h-24 w-full" />
          ) : !installURL.data?.configured ? (
            <p className="text-sm text-[var(--text-secondary)]">
              GitHub App not configured on this kuso instance.
            </p>
          ) : (installs.data ?? []).length === 0 ? (
            <div className="space-y-2">
              <p className="text-sm text-[var(--text-secondary)]">
                No GitHub installations yet.
              </p>
              <a
                href={installURL.data.url}
                target="_blank"
                rel="noreferrer"
                className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-3 text-xs font-medium hover:bg-[var(--accent-subtle)]"
              >
                <Github className="h-3.5 w-3.5" />
                Install kuso GitHub App
              </a>
            </div>
          ) : !picked ? (
            <div className="space-y-2">
              <Input
                type="search"
                value={repoQuery}
                onChange={(e) => setRepoQuery(e.target.value)}
                placeholder={`Filter ${allRepos.length} repositories…`}
                className="h-8 font-mono text-[12px]"
                autoFocus
              />
              <ul className="max-h-72 overflow-auto divide-y divide-[var(--border-subtle)] rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]">
                {filteredRepos.length === 0 ? (
                  <li className="px-3 py-2 text-xs text-[var(--text-tertiary)]">
                    No repos match{repoQuery ? ` "${repoQuery}"` : ""}.
                  </li>
                ) : (
                  filteredRepos.map(({ installationId, repo }) => (
                    <li key={`${installationId}/${repo.fullName}`}>
                      <button
                        type="button"
                        onClick={() => setPicked({ installationId, repo })}
                        className="flex w-full items-center justify-between gap-3 px-3 py-1.5 text-left text-[12px] hover:bg-[var(--bg-tertiary)]"
                      >
                        <span className="flex items-center gap-2 truncate">
                          <Github className="h-3 w-3 text-[var(--text-tertiary)]" />
                          <span className="font-mono truncate">{repo.fullName}</span>
                          {repo.private && (
                            <span className="rounded bg-[var(--bg-tertiary)] px-1 py-0.5 font-mono text-[9px] text-[var(--text-tertiary)]">
                              private
                            </span>
                          )}
                        </span>
                        <ArrowRight className="h-3 w-3 text-[var(--text-tertiary)] shrink-0" />
                      </button>
                    </li>
                  ))
                )}
              </ul>
            </div>
          ) : (
            <div className="flex items-center justify-between rounded-md border border-[var(--accent)]/40 bg-[var(--accent-subtle)] px-3 py-2 text-[12px]">
              <span className="flex items-center gap-2 truncate">
                <Check className="h-3.5 w-3.5 text-[var(--accent)]" />
                <span className="font-mono truncate">{picked.repo.fullName}</span>
                <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
                  {picked.repo.defaultBranch}
                </span>
              </span>
              <button
                type="button"
                onClick={() => {
                  setPicked(null);
                  setName("");
                  setPath("");
                  setReason(null);
                }}
                className="font-mono text-[10px] text-[var(--text-secondary)] underline"
              >
                change
              </button>
            </div>
          )}
        </div>
      </section>

      {picked && (
        <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
          <div className="border-b border-[var(--border-subtle)] px-4 py-2.5">
            <h2 className="flex items-center gap-2 text-sm font-semibold tracking-tight">
              <RuntimeIcon runtime={runtime} />
              Service
            </h2>
          </div>
          <div className="space-y-3 px-4 py-3">
            <div className="grid grid-cols-2 gap-3">
              <Field label="name">
                <Input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  className="h-8 font-mono text-[12px]"
                />
              </Field>
              <Field label="port" hint="container port">
                <Input
                  type="number"
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
                  placeholder="auto"
                  className="h-8 font-mono text-[12px]"
                />
              </Field>
            </div>
            <Field label="path" hint="monorepo subdir; root if empty">
              <Input
                value={path}
                onChange={(e) => setPath(e.target.value)}
                placeholder="apps/api"
                className="h-8 font-mono text-[12px]"
              />
            </Field>
            <Field label="runtime">
              <div className="inline-flex flex-wrap gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
                {["dockerfile", "nixpacks", "static", "buildpacks"].map((r) => (
                  <button
                    key={r}
                    type="button"
                    onClick={() => setRuntime(r)}
                    className={
                      "rounded px-2 py-1 font-mono text-[11px] " +
                      (runtime === r
                        ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                        : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]")
                    }
                  >
                    {r}
                  </button>
                ))}
              </div>
            </Field>
            {reason && (
              <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
                detected: {reason}
              </p>
            )}
          </div>
          <footer className="flex items-center justify-between border-t border-[var(--border-subtle)] px-4 py-3">
            <Link
              href={`/projects/${encodeURIComponent(project)}`}
              className="font-mono text-[10px] text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
            >
              ← cancel
            </Link>
            <Button type="button" size="sm" onClick={onAdd} disabled={submitting}>
              <Plus className="h-3.5 w-3.5" />
              {submitting ? "Adding…" : "Add service"}
            </Button>
          </footer>
        </section>
      )}
    </div>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        {label}
      </div>
      {children}
      {hint && <div className="text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>}
    </div>
  );
}
