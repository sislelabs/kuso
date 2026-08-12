// Small formatting helpers used throughout the dashboard.

export function relativeTime(input?: string): string {
  if (!input) return "";
  const t = new Date(input).getTime();
  if (Number.isNaN(t)) return "";
  const diffMs = Date.now() - t;
  const sec = Math.floor(diffMs / 1000);
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.floor(hr / 24);
  if (day < 30) return `${day}d ago`;
  const mo = Math.floor(day / 30);
  if (mo < 12) return `${mo}mo ago`;
  return new Date(input).toLocaleDateString();
}

export function shortSha(s?: string, n = 7): string {
  if (!s) return "";
  return s.slice(0, n);
}

// stripRepoCredentials removes embedded credentials (userinfo) from a
// git clone URL before DISPLAY. Users store deploy-token URLs like
//   https://kuso-deploy:gldt-xxxx@gitlab.com/org/repo.git
// so the builder can clone — but any surface that renders the stored
// URL verbatim (project card, service settings label) was printing a
// working credential to every viewer.
//
// Colon-free userinfo is kept only under the SSH family (ssh:// and
// scp-style git@host:path), where it's a username the clone needs.
// Under http(s) — or a schemeless slash-path form — a colon-free
// userinfo is a bare token (https://TOKEN@github.com/… is GitHub's
// documented PAT shape), so it strips like any other credential.
// Mirrors server-go/internal/kube/repo_url.go StripRepoURLCredentials.
export function stripRepoCredentials(raw?: string): string {
  if (!raw) return "";
  // Userinfo is [^/]+ (not [^/@]+) so a password containing @ strips
  // to the LAST @ before the path, leaving no credential tail. Trim:
  // stray paste whitespace must not defeat the anchored match.
  const m = raw.trim().match(/^([a-z][a-z0-9+.-]*:\/\/)?([^/]+)@(.+)$/i);
  if (!m) return raw;
  const [, scheme = "", userinfo, rest] = m;
  if (!userinfo.includes(":")) {
    if (/^(ssh|git\+ssh|git):\/\/$/i.test(scheme)) return raw;
    // scp-style git@host:path (no scheme, colon before any slash).
    if (!scheme && /^[^/:]+:/.test(rest)) return raw;
  }
  return scheme + rest;
}
