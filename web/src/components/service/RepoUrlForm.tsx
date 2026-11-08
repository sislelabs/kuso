"use client";

import { useState } from "react";
import { Link2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { classifyRepoReadFailure, inspectRepo } from "@/features/github";
import { ApiError } from "@/lib/api-client";

export interface RepoByUrl {
  url: string;
  // Empty for public repos.
  token: string;
  // "owner/repo", for display and the default service name.
  fullName: string;
  defaultBranch: string;
  branches: string[];
}

// repoFullName turns https://host/owner/repo(.git) into "owner/repo".
export function repoFullName(url: string): string {
  try {
    return new URL(url).pathname.replace(/^\/+|\/+$/g, "").replace(/\.git$/, "");
  } catch {
    return url;
  }
}

// RepoUrlForm adds a service from a repo URL with no GitHub App: the
// server reads the repo's branches straight from the git host. A private
// repo needs an access token, which is stored with the service.
//
// mode "public" has no token field (onNeedToken lets the host switch to
// a token form on a 404); "token" always asks for one; omitted keeps the
// original reveal-on-demand behaviour.
export function RepoUrlForm({
  onPicked,
  mode,
  initialUrl = "",
  onNeedToken,
}: {
  onPicked: (repo: RepoByUrl) => void;
  mode?: "public" | "token";
  initialUrl?: string;
  onNeedToken?: (url: string) => void;
}) {
  const [url, setUrl] = useState(initialUrl);
  const [token, setToken] = useState("");
  const [showToken, setShowToken] = useState(mode === "token");
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const check = async () => {
    const repoURL = url.trim();
    if (!/^https?:\/\//i.test(repoURL)) {
      setError("Enter the repository's https URL, e.g. https://github.com/owner/repo.");
      return;
    }
    if (mode === "token" && !token.trim()) {
      setError("Paste an access token with read access to the repository.");
      return;
    }
    setChecking(true);
    setError(null);
    try {
      const info = await inspectRepo({ url: repoURL, token: token.trim() || undefined });
      if (!info.defaultBranch) {
        setError("That repository has no branches yet.");
        return;
      }
      onPicked({
        url: repoURL,
        token: token.trim(),
        fullName: repoFullName(repoURL),
        defaultBranch: info.defaultBranch,
        branches: info.branches,
      });
    } catch (e) {
      // A 404 is the common case (typo, or a private repo): offer the
      // token field instead of making the user hunt for it.
      const notFound = e instanceof ApiError && e.status === 404;
      if (notFound && mode !== "public") setShowToken(true);
      if (notFound && mode === "public") {
        setError("Couldn't find a public repository at that URL. If it's private, use an access token.");
      } else {
        const f = classifyRepoReadFailure({
          via: "url",
          repo: repoFullName(repoURL),
          branch: "",
          hasToken: !!token.trim(),
          error: e,
        });
        setError(`${f.title}. ${f.detail}`);
      }
    } finally {
      setChecking(false);
    }
  };

  return (
    <form
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault();
        void check();
      }}
    >
      <div className="flex items-center gap-2">
        <Input
          value={url}
          onChange={(e) => {
            setUrl(e.target.value);
            setError(null);
          }}
          placeholder="https://github.com/owner/repo"
          aria-label="Repository URL"
          aria-invalid={error ? true : undefined}
          spellCheck={false}
          className="h-8 flex-1 font-mono text-xs"
        />
        <Button
          type="submit"
          size="sm"
          variant="outline"
          disabled={checking || !url.trim()}
          disabledReason={!checking && !url.trim() ? "Paste a repository URL first" : undefined}
        >
          <Link2 className="h-3.5 w-3.5" />
          {checking ? "Checking…" : "Use this repo"}
        </Button>
      </div>
      {mode === "public" ? null : showToken ? (
        <Input
          type="password"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder="Access token, e.g. github_pat_… or glpat-…"
          aria-label="Repository access token"
          autoComplete="off"
          spellCheck={false}
          data-1p-ignore
          data-lpignore="true"
          className="h-8 font-mono text-xs"
        />
      ) : (
        <button
          type="button"
          onClick={() => setShowToken(true)}
          className="text-xs text-[var(--link)] underline-offset-4 hover:underline"
        >
          Private repo? Add an access token
        </button>
      )}
      {error && (
        <p role="alert" className="text-xs text-[var(--error)]">
          {error}
          {mode === "public" && onNeedToken && /private/.test(error) && (
            <>
              {" "}
              <button type="button" className="font-medium underline underline-offset-2" onClick={() => onNeedToken(url.trim())}>
                Use an access token
              </button>
            </>
          )}
        </p>
      )}
      <p className="text-hint">
        {mode === "token"
          ? "A fine-grained token with read-only Contents access is enough (GitHub github_pat_…, GitLab glpat-…). It's stored with the service and used only to clone."
          : "No GitHub App needed. To deploy on push, enable the deploy hook in the service's settings afterwards."}
      </p>
    </form>
  );
}
