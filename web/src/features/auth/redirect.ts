// safeRedirectTarget gates the login ?next= bounce. It returns a
// same-origin path (pathname + search + hash) or null.
//
// A prefix check isn't enough: the WHATWG URL parser (which the Next
// router uses) strips tab/CR/LF, so "/\t/evil.com" passes a "starts
// with a single /" test yet resolves to https://evil.com/. Resolving
// the candidate the same way the router will and comparing origins
// closes that whole class.
export function safeRedirectTarget(raw: unknown, origin: string): string | null {
  if (typeof raw !== "string" || !raw.startsWith("/")) return null;
  let u: URL;
  try {
    u = new URL(raw, origin);
  } catch {
    return null;
  }
  if (u.origin !== origin) return null;
  return u.pathname + u.search + u.hash;
}
