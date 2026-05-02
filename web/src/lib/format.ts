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
