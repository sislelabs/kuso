"use client";

import { useState } from "react";
import { Link2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { inspectRepo } from "@/features/github";
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
export function RepoUrlForm({ onPicked }: { onPicked: (repo: RepoByUrl) => void }) {
  const [url, setUrl] = useState("");
  const [token, setToken] = useState("");
  const [showToken, setShowToken] = useState(false);
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const check = async () => {
    const repoURL = url.trim();
    if (!/^https?:\/\//i.test(repoURL)) {
      setError("Enter the repository's https URL, e.g. https://github.com/owner/repo.");
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
      if (e instanceof ApiError && e.status === 404) setShowToken(true);
      setError(e instanceof Error ? e.message : "Couldn't read that repository.");
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
          className="h-8 flex-1 font-mono text-[12px]"
        />
        <Button type="submit" size="sm" variant="outline" disabled={checking || !url.trim()}>
          <Link2 className="h-3.5 w-3.5" />
          {checking ? "Checking…" : "Use this repo"}
        </Button>
      </div>
      {showToken ? (
        <Input
          type="password"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder="access token for a private repo (github_pat_… / glpat-…)"
          aria-label="Repository access token"
          autoComplete="off"
          spellCheck={false}
          data-1p-ignore
          data-lpignore="true"
          className="h-8 font-mono text-[12px]"
        />
      ) : (
        <button
          type="button"
          onClick={() => setShowToken(true)}
          className="font-mono text-[10px] text-[var(--text-secondary)] underline"
        >
          private repo? add an access token
        </button>
      )}
      {error && (
        <p role="alert" className="text-[11px] text-[var(--error)]">
          {error}
        </p>
      )}
      <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
        No GitHub App needed. To deploy on push, enable the deploy hook in the service&apos;s
        settings afterwards.
      </p>
    </form>
  );
}
