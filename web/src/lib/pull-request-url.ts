import { stripRepoCredentials } from "@/lib/format";

/**
 * Web URL of pull request `n` on a GitHub repo, or undefined when the
 * repo isn't on github.com (GitLab calls them merge requests, and other
 * hosts have no fixed path). Accepts https, scp-style and .git forms.
 */
export function pullRequestUrl(repoUrl: string | undefined, n: number | undefined): string | undefined {
  if (!repoUrl || !n) return undefined;
  const clean = stripRepoCredentials(repoUrl).trim();
  const m = clean.match(/github\.com[/:]([^/\s]+)\/([^/\s]+?)(?:\.git)?\/?$/i);
  if (!m) return undefined;
  return `https://github.com/${m[1]}/${m[2]}/pull/${n}`;
}
