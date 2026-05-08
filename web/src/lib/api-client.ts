import { env } from "./env";

export class ApiError extends Error {
  status: number;
  body: unknown;
  constructor(status: number, body: unknown, message: string) {
    // Prefer the response body when it carries a useful string —
    // the server returns "addon foo/bar already exists" (text) on
    // 409, and surfacing that beats a bare "409 Conflict". JSON
    // objects with .error or .message are also unwrapped. Falls
    // back to the raw status text when nothing better is there.
    const friendly =
      (typeof body === "string" && body.trim() !== "" && body.trim()) ||
      (body && typeof body === "object" && "error" in body && typeof (body as { error: unknown }).error === "string" && (body as { error: string }).error) ||
      (body && typeof body === "object" && "message" in body && typeof (body as { message: unknown }).message === "string" && (body as { message: string }).message) ||
      message;
    super(friendly as string);
    this.status = status;
    this.body = body;
  }
}

// captureJwtFromFragment runs once on first load. The OAuth callback
// redirects to "/#token=<jwt>"; we drop it on the floor — the
// HttpOnly cookie the server set in the same response carries the
// session. We just scrub the fragment so the token doesn't linger
// in browser history or get copied into a chat.
function captureJwtFromFragment() {
  if (typeof window === "undefined") return;
  const hash = window.location.hash;
  if (!hash || !hash.startsWith("#")) return;
  const params = new URLSearchParams(hash.slice(1));
  if (!params.has("token")) return;
  const clean = window.location.pathname + window.location.search;
  window.history.replaceState(null, "", clean);
}

// getJwt is a no-op for the SPA — sessions live in the HttpOnly
// cookie, JS can't read them. Kept on the API surface only because
// the WebSocket log-tail handshake needs to pass the token in
// Sec-WebSocket-Protocol (browsers can't set Authorization on the
// upgrade); that path now reads document.cookie's non-HttpOnly
// fallback. New installs return "" here and the WS path falls
// through to cookie-mode auth.
export function getJwt(): string | null {
  if (typeof window === "undefined") return null;
  captureJwtFromFragment();
  return null;
}

// clearJwt asks the server to drop the session cookie. POST /auth/logout
// sets Max-Age=-1 so the browser evicts it. The previous local-storage
// path is gone.
export async function clearJwt() {
  if (typeof window === "undefined") return;
  try {
    await fetch(`${env.apiBase}/api/auth/logout`, {
      method: "POST",
      credentials: "include",
    });
  } catch {
    /* network — UI clears state regardless */
  }
}

// setJwt is a kept-name shim for the local-login flow. The server
// also sets the HttpOnly cookie in the same response; this function
// exists only so the auth hook's onSuccess can call it without a
// conditional. No-op in v0.10.
export function setJwt(_token: string) { /* cookie-managed */ }

// Cache-bust on server roll. Every API response carries
// X-Kuso-Server-Version. We pin the first value seen for the session
// and watch for drift; once observed, the next route change in the
// app triggers a hard reload so the browser picks up the new JS
// chunks. We don't reload mid-action — a save-in-flight shouldn't
// vanish — only on the next user-initiated navigation.
//
// `versionMismatch` is set once and never unset. It's read by the
// router-level <ServerVersionGuard /> on each pathname change.
let firstServerVersion: string | null = null;
let versionMismatch = false;
const versionListeners = new Set<() => void>();

function observeServerVersion(v: string | null) {
  if (!v) return;
  if (firstServerVersion === null) {
    firstServerVersion = v;
    return;
  }
  if (v !== firstServerVersion && !versionMismatch) {
    versionMismatch = true;
    versionListeners.forEach((cb) => {
      try {
        cb();
      } catch {
        /* listener errors must not break the api call */
      }
    });
  }
}

export function getServerVersionMismatch(): boolean {
  return versionMismatch;
}

export function getPinnedServerVersion(): string | null {
  return firstServerVersion;
}

export function onServerVersionMismatch(cb: () => void): () => void {
  versionListeners.add(cb);
  if (versionMismatch) cb();
  return () => versionListeners.delete(cb);
}

type Options = Omit<RequestInit, "body"> & { body?: unknown };

export async function api<T>(path: string, opts: Options = {}): Promise<T> {
  const headers = new Headers(opts.headers);
  if (opts.body !== undefined && !(opts.body instanceof FormData)) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`${env.apiBase}${path}`, {
    ...opts,
    headers,
    body:
      opts.body === undefined
        ? undefined
        : opts.body instanceof FormData
          ? opts.body
          : JSON.stringify(opts.body),
    // Cookie-only session: every API call rides the kuso.JWT_TOKEN
    // cookie via credentials:include. No Authorization header.
    credentials: "include",
  });
  observeServerVersion(res.headers.get("X-Kuso-Server-Version"));
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let parsed: unknown = undefined;
  if (text.length > 0) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = text;
    }
  }
  if (!res.ok) {
    throw new ApiError(res.status, parsed, `${res.status} ${res.statusText}`);
  }
  return parsed as T;
}
