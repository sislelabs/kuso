"use client";

import { useEffect, useMemo, useState, type Ref } from "react";
import Link from "next/link";
import { ArrowRight, Github, KeyRound, Link2, Lock } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  repoOwner,
  repoOwners,
  sortReposByPush,
  useGithubRepos,
  useInstallations,
  useInstallURL,
} from "@/features/github";
import { relativeTime } from "@/lib/format";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { Perms, useCan, useSession } from "@/features/auth";
import { ApiError } from "@/lib/api-client";
import { RepoUrlForm, type RepoByUrl } from "@/components/service/RepoUrlForm";
import { TabPanel, Tabs } from "@/components/ui/tabs";

type SourceTab = "app" | "url" | "token";

// A repo pick, normalised across the admin (installations) and
// non-admin (flat /api/github/repos) sources.
export interface PickableRepo {
  fullName: string;
  defaultBranch: string;
  private?: boolean;
  pushedAt?: string;
}

// PickedRepo is the repo a service builds from. byUrl is set when the
// user pasted a URL instead of picking through the GitHub App: there is
// no installation (installationId 0) and the clone is anonymous or uses
// the access token.
export interface PickedRepo {
  installationId: number;
  repo: PickableRepo;
  byUrl?: { url: string; token: string; branches: string[] };
}

export function pickedRepoURL(p: PickedRepo): string {
  return p.byUrl?.url ?? `https://github.com/${p.repo.fullName}`;
}

// useRepoSources loads the repos the GitHub App can see. Admins read
// installations (carries the private flag); everyone else gets the
// projects:create-gated flat list.
export function useRepoSources() {
  const installURL = useInstallURL();
  const session = useSession();
  const isAdmin = useCan(Perms.SettingsAdmin);
  const sessionReady = !!session.data;
  const installs = useInstallations({ enabled: sessionReady && isAdmin });
  const flatRepos = useGithubRepos({ enabled: sessionReady && !isAdmin });
  const reposQuery = isAdmin ? installs : flatRepos;
  const allRepos = useMemo((): { installationId: number; repo: PickableRepo }[] => {
    if (isAdmin) {
      return sortReposByPush(
        (installs.data ?? []).flatMap((inst) =>
          inst.repositories.map((r) => ({ installationId: inst.id, repo: r })),
        ),
      );
    }
    return sortReposByPush(
      (flatRepos.data ?? []).map((r) => ({
        installationId: r.installationId,
        repo: { fullName: r.fullName, defaultBranch: r.defaultBranch, pushedAt: r.pushedAt },
      })),
    );
  }, [isAdmin, installs.data, flatRepos.data]);
  return {
    isAdmin,
    sessionReady,
    appConfigured: !!installURL.data?.configured,
    installURL: installURL.data?.url ?? "",
    loading: !sessionReady || installURL.isPending || reposQuery.isLoading,
    forbidden: reposQuery.error instanceof ApiError && reposQuery.error.status === 403,
    isError: reposQuery.isError,
    refetch: () => void reposQuery.refetch(),
    allRepos,
  };
}

// RepoPicker lists the App's repos with a filter, and always offers
// paste-a-URL underneath so an install without the App (or a repo the
// App can't see) still has a way in.
export function RepoPicker({
  onPick,
  searchRef,
  error,
  autoFocus,
  listClassName = "max-h-72",
}: {
  onPick: (p: PickedRepo) => void;
  searchRef?: Ref<HTMLInputElement>;
  error?: string;
  autoFocus?: boolean;
  listClassName?: string;
}) {
  const src = useRepoSources();
  const [query, setQuery] = useState("");
  // Start on the GitHub App list when it has repos, else on a URL form.
  const hasAppRepos = src.appConfigured && src.allRepos.length > 0;
  const [tab, setTab] = useState<SourceTab | null>(null);
  const [handoffUrl, setHandoffUrl] = useState("");
  useEffect(() => {
    if (tab === null && !src.loading) setTab(hasAppRepos ? "app" : "url");
  }, [tab, src.loading, hasAppRepos]);
  const activeTab: SourceTab = tab ?? "app";
  const pickByUrl = (r: RepoByUrl) =>
    onPick({
      installationId: 0,
      repo: { fullName: r.fullName, defaultBranch: r.defaultBranch },
      byUrl: { url: r.url, token: r.token, branches: r.branches },
    });
  const owners = useMemo(() => repoOwners(src.allRepos), [src.allRepos]);
  const [owner, setOwner] = useState("");
  // An owner that disappeared (App uninstalled from it) falls back to all.
  const activeOwner = owners.includes(owner) ? owner : "";
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return src.allRepos.filter(
      ({ repo }) =>
        (!activeOwner || repoOwner(repo.fullName) === activeOwner) &&
        (!q || repo.fullName.toLowerCase().includes(q)),
    );
  }, [src.allRepos, query, activeOwner]);

  let appPanel: React.ReactNode;
  if (src.loading) {
    appPanel = <Skeleton className="h-40 w-full" />;
  } else if (!src.appConfigured) {
    appPanel = (
      <div className="rounded-md border border-dashed border-[var(--border-subtle)] bg-[var(--bg-primary)] px-4 py-4">
        <p className="flex items-center gap-2 text-sm font-medium">
          <Github className="h-4 w-4" />
          GitHub isn&apos;t connected yet
        </p>
        {src.isAdmin ? (
          <>
            <p className="mt-1 text-xs leading-relaxed text-[var(--text-secondary)]">
              Connect the kuso GitHub App to browse your repositories, deploy on every push and get a
              preview URL for each pull request. Public repos work without it: use the Public URL tab.
            </p>
            <Link
              href="/settings/github"
              className="mt-3 inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-3 text-xs font-medium hover:bg-[var(--accent-subtle)]"
            >
              <Github className="h-3.5 w-3.5" />
              Connect GitHub
            </Link>
          </>
        ) : (
          <p className="mt-1 text-xs leading-relaxed text-[var(--text-secondary)]">
            An admin needs to connect the kuso GitHub App before repositories can be listed here. You
            can still deploy from a repository URL.
          </p>
        )}
      </div>
    );
  } else if (src.forbidden) {
    appPanel = (
      <p className="text-sm text-[var(--text-secondary)]">
        You can&apos;t browse repositories. Ask an admin for project-create access, or use a repository URL.
      </p>
    );
  } else if (src.isError) {
    appPanel = (
      <p role="alert" className="text-sm text-[var(--error)]">
        Couldn&apos;t load repositories.{" "}
        <button type="button" className="underline" onClick={src.refetch}>
          Retry
        </button>
      </p>
    );
  } else if (src.allRepos.length === 0) {
    appPanel = src.isAdmin ? (
      <div className="rounded-md border border-dashed border-[var(--border-subtle)] bg-[var(--bg-primary)] px-4 py-4">
        <p className="text-sm text-[var(--text-secondary)]">
          The GitHub App isn&apos;t installed on any account yet. Install it and pick the repositories
          kuso may read.
        </p>
        {src.installURL && (
          <a
            href={src.installURL}
            target="_blank"
            rel="noreferrer"
            className="mt-3 inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-3 text-xs font-medium hover:bg-[var(--accent-subtle)]"
          >
            <Github className="h-3.5 w-3.5" />
            Install the GitHub App
          </a>
        )}
      </div>
    ) : (
      <p className="text-sm text-[var(--text-secondary)]">
        No repositories are connected. Ask an admin to install the GitHub App, or use a repository URL.
      </p>
    );
  } else {
    const ownerFilter =
      owners.length < 2 ? null : owners.length <= 3 ? (
        <Segmented
          label="Repository owner"
          size="sm"
          className="shrink-0 self-start sm:self-auto"
          value={activeOwner}
          onChange={setOwner}
          options={[
            { value: "", label: "All" },
            ...owners.map((o) => ({ value: o, label: o })),
          ]}
        />
      ) : (
        <Select
          aria-label="Repository owner"
          value={activeOwner}
          onChange={(e) => setOwner(e.target.value)}
          wrapperClassName="sm:w-44 sm:shrink-0"
        >
          <option value="">All owners</option>
          {owners.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </Select>
      );
    appPanel = (
      <div className="space-y-2">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <Input
            ref={searchRef}
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={`Search ${src.allRepos.length} ${src.allRepos.length === 1 ? "repository" : "repositories"}…`}
            aria-label="Search repositories"
            aria-invalid={error ? true : undefined}
            data-primary-search
            data-no-draft
            className="h-9 min-w-0 flex-1 text-[13px]"
            autoFocus={autoFocus}
          />
          {ownerFilter}
        </div>
        <ul
          className={
            "divide-y divide-[var(--border-subtle)] overflow-auto rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] " +
            listClassName
          }
        >
          {filtered.length === 0 ? (
            <li className="px-3 py-3 text-xs text-[var(--text-secondary)]">
              No repositories{activeOwner ? ` from ${activeOwner}` : ""}
              {query.trim() ? <> match &ldquo;{query}&rdquo;</> : null}. Not listed? Use the URL or token tab, or add it
              to the GitHub App&apos;s repository access.
            </li>
          ) : (
            filtered.map(({ installationId, repo }) => (
              <li key={`${installationId}/${repo.fullName}`}>
                <button
                  type="button"
                  onClick={() => onPick({ installationId, repo })}
                  className="group flex w-full items-center justify-between gap-3 px-3 py-2 text-left hover:bg-[var(--bg-tertiary)] focus-visible:bg-[var(--bg-tertiary)] focus-visible:outline-none"
                >
                  <span className="flex min-w-0 items-center gap-2">
                    <Github className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
                    <span className="truncate font-mono text-[13px]">{repo.fullName}</span>
                    {repo.private && (
                      <Lock className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" aria-label="private" />
                    )}
                  </span>
                  <span className="flex shrink-0 items-center gap-3 text-xs text-[var(--text-tertiary)]">
                    {repo.pushedAt && relativeTime(repo.pushedAt) && (
                      <time dateTime={repo.pushedAt} title={new Date(repo.pushedAt).toLocaleString()}>
                        <span className="max-sm:sr-only">pushed </span>
                        {relativeTime(repo.pushedAt)}
                      </time>
                    )}
                    <span className="flex items-center gap-1 group-hover:text-[var(--text-primary)]">
                      <span className="max-sm:sr-only">Import</span>
                      <ArrowRight className="h-3 w-3" />
                    </span>
                  </span>
                </button>
              </li>
            ))
          )}
        </ul>
      </div>
    );
  }

  const idBase = "repo-source";
  return (
    <div className="space-y-4">
      {error && (
        <p role="alert" className="text-xs text-[var(--error)]">
          {error}
        </p>
      )}
      <Tabs
        ariaLabel="Repository source"
        idBase={idBase}
        size="sm"
        value={activeTab}
        onValueChange={(v) => setTab(v as SourceTab)}
        items={[
          { value: "app", label: "GitHub App", icon: <Github /> },
          { value: "url", label: "Public URL", icon: <Link2 /> },
          { value: "token", label: "Access token", icon: <KeyRound /> },
        ]}
      />
      <TabPanel idBase={idBase} value="app" active={activeTab === "app"}>
        {appPanel}
      </TabPanel>
      <TabPanel idBase={idBase} value="url" active={activeTab === "url"} className="space-y-2">
        <p className="text-xs text-[var(--text-secondary)]">
          Any public repository on GitHub, GitLab, Gitea or another https git host.
        </p>
        {src.sessionReady && (
          <RepoUrlForm
            mode="public"
            onPicked={pickByUrl}
            onNeedToken={(url) => {
              setHandoffUrl(url);
              setTab("token");
            }}
          />
        )}
      </TabPanel>
      <TabPanel idBase={idBase} value="token" active={activeTab === "token"} className="space-y-2">
        <p className="text-xs text-[var(--text-secondary)]">
          A private repository the GitHub App can&apos;t see, or one on another git host.
        </p>
        {src.sessionReady && (
          <RepoUrlForm key={handoffUrl} mode="token" initialUrl={handoffUrl} onPicked={pickByUrl} />
        )}
      </TabPanel>
    </div>
  );
}
