// Small formatting helpers used throughout the dashboard.

export function relativeTime(input?: string, now: number = Date.now()): string {
  if (!input) return "";
  const t = new Date(input).getTime();
  if (Number.isNaN(t)) return "";
  const diffMs = now - t;
  // Future timestamps (invite/token expiry) read "in 3d"; a few seconds
  // of client/server clock skew on a fresh event reads "just now"
  // rather than "-3s ago".
  const future = diffMs < 0;
  const sec = Math.floor(Math.abs(diffMs) / 1000);
  if (sec < 60) return future || sec < 5 ? "just now" : `${sec}s ago`;
  const fmt = (n: number, unit: string) => (future ? `in ${n}${unit}` : `${n}${unit} ago`);
  const min = Math.floor(sec / 60);
  if (min < 60) return fmt(min, "m");
  const hr = Math.floor(min / 60);
  if (hr < 24) return fmt(hr, "h");
  const day = Math.floor(hr / 24);
  if (day < 30) return fmt(day, "d");
  const mo = Math.floor(day / 30);
  if (mo < 12) return fmt(mo, "mo");
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

// formatCpu is the one CPU format: millicores below one core
// ("3 mCPU", "250 mCPU"), cores from there ("1 core", "1.3 cores",
// "12 cores"). Idle pods read "<1 mCPU", never "0".
export function formatCpu(milli: number): string {
  if (!Number.isFinite(milli) || milli <= 0) return "0 mCPU";
  if (milli < 1) return "<1 mCPU";
  if (milli < 999.5) return `${Math.round(milli)} mCPU`;
  const cores = milli / 1000;
  const v = cores >= 10 ? cores.toFixed(0) : cores.toFixed(1).replace(/\.0$/, "");
  return `${v} ${v === "1" ? "core" : "cores"}`;
}

// formatLatency: "4 ms", "1.2 s".
export function formatLatency(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  if (ms < 1000) return `${Math.max(0, Math.round(ms))} ms`;
  return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0).replace(/\.0$/, "")} s`;
}
