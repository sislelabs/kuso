// GitHub deep links for deploy rows. Only github.com repos get links; any
// other host (GitLab, self-hosted Gitea) returns null and the UI shows the
// SHA as plain text.

const SHA_RE = /^[0-9a-f]{7,40}$/i;

export function githubRepoSlug(url: string | undefined): string | null {
  if (!url) return null;
  const trimmed = url.trim().replace(/\/+$/, "").replace(/\.git$/, "");
  const ssh = /^git@github\.com:([^/\s]+)\/([^/\s]+)$/i.exec(trimmed);
  if (ssh) return `${ssh[1]}/${ssh[2]}`;
  try {
    const u = new URL(trimmed.includes("://") ? trimmed : `https://${trimmed}`);
    if (u.hostname.toLowerCase() !== "github.com" && u.hostname.toLowerCase() !== "www.github.com") return null;
    const [owner, repo] = u.pathname.split("/").filter(Boolean);
    return owner && repo ? `${owner}/${repo}` : null;
  } catch {
    return null;
  }
}

export function githubCommitUrl(repoUrl: string | undefined, sha: string | undefined): string | null {
  const slug = githubRepoSlug(repoUrl);
  if (!slug || !sha || !SHA_RE.test(sha)) return null;
  return `https://github.com/${slug}/commit/${sha}`;
}

export function githubCompareUrl(
  repoUrl: string | undefined,
  fromSha: string | undefined,
  toSha: string | undefined,
): string | null {
  const slug = githubRepoSlug(repoUrl);
  if (!slug || !fromSha || !toSha || !SHA_RE.test(fromSha) || !SHA_RE.test(toSha)) return null;
  if (fromSha === toSha) return null;
  return `https://github.com/${slug}/compare/${fromSha}...${toSha}`;
}
