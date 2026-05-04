// Client-side slugifier for service display-name → URL slug. Mirrors
// the server's projects.SlugifyServiceName rules so the AddService
// dialog can preview the URL the server will assign before submit.
// Server is still authoritative — any drift just means the user sees
// a slightly different slug after the API call returns, not a broken
// state. Rules: lowercase, runs of separators → single dash,
// non-alphanumeric dropped, leading/trailing dash trimmed, ≤30 chars.
export function slugifyServiceName(input: string): string {
  const lowered = input.trim().toLowerCase();
  let out = "";
  let prevDash = true; // suppress leading dash
  for (const ch of lowered) {
    const code = ch.charCodeAt(0);
    const alpha = code >= 97 && code <= 122; // a-z
    const digit = code >= 48 && code <= 57; // 0-9
    if (alpha || digit) {
      out += ch;
      prevDash = false;
    } else if (ch === "-" || ch === " " || ch === "_" || ch === ".") {
      if (!prevDash) {
        out += "-";
        prevDash = true;
      }
    }
    // anything else dropped
  }
  // trim trailing dashes + cap at 30
  out = out.replace(/-+$/g, "");
  if (out.length > 30) {
    out = out.slice(0, 30).replace(/-+$/g, "");
  }
  return out;
}
