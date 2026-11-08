import { getPinnedServerVersion } from "@/lib/api-client";

// compareVersions orders "v0.36.0"-style versions (pre-release suffixes
// ignored). Returns null when either side doesn't parse.
export function compareVersions(a: string, b: string): number | null {
  const parse = (v: string) => {
    const m = /^v?(\d+)\.(\d+)\.(\d+)/.exec(v.trim());
    return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null;
  };
  const x = parse(a);
  const y = parse(b);
  if (!x || !y) return null;
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] - y[i];
  return 0;
}

// unavailableNotice is the one wording for "this server doesn't answer
// the endpoint behind this feature". It never tells the reader which
// version to install: the endpoint's absence is the fact, a version
// constant is only a guess (a dev build can report the same version
// without it). `since` only decides between "yet" and "on this server".
export function unavailableNotice(feature: string, opts: { since?: string; version?: string | null; plural?: boolean } = {}): string {
  const is = opts.plural ? "aren't" : "isn't";
  const v = opts.version === undefined ? getPinnedServerVersion() : opts.version;
  if (!v) return `${feature} ${is} available from this server yet.`;
  const cmp = opts.since ? compareVersions(v, opts.since) : null;
  if (cmp !== null && cmp >= 0) return `${feature} ${is} available on this server (kuso ${v}).`;
  return `${feature} ${is} available from this server yet (kuso ${v}).`;
}
