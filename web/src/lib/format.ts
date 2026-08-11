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
// working credential to every viewer. Handles scheme-ful and bare
// forms; scp-style SSH (git@host:org/repo) is left alone — its
// userinfo is a username, not a secret.
export function stripRepoCredentials(raw?: string): string {
  if (!raw) return "";
  const m = raw.match(/^([a-z][a-z0-9+.-]*:\/\/)?([^/@]+)@(.+)$/i);
  if (!m) return raw;
  // scp-style (git@host:path, no scheme, no password) → keep as-is.
  if (!m[1] && !m[2].includes(":")) return raw;
  return (m[1] ?? "") + m[3];
}
