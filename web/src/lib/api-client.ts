import { env } from "./env";

const JWT_KEY = "kuso.jwt";

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
// redirects to "/#token=<jwt>"; we lift the token into localStorage
// and scrub the fragment from the URL so it doesn't linger in
// browser history or get copied into a chat. The HttpOnly cookie
// the server set in the same response stays put for the WebSocket
// log-tail handshake (where the SPA can't set Authorization).
function captureJwtFromFragment() {
  if (typeof window === "undefined") return;
  const hash = window.location.hash;
  if (!hash || !hash.startsWith("#")) return;
  const params = new URLSearchParams(hash.slice(1));
  const tok = params.get("token");
  if (!tok) return;
  try {
    window.localStorage.setItem(JWT_KEY, tok);
  } catch {
    /* private mode / quota */
  }
  // Replace location to wipe the fragment without triggering a reload.
  const clean = window.location.pathname + window.location.search;
  window.history.replaceState(null, "", clean);
}

export function getJwt(): string | null {
  if (typeof window === "undefined") return null;
  captureJwtFromFragment();
  return window.localStorage.getItem(JWT_KEY);
}

export function setJwt(token: string) {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(JWT_KEY, token);
  // The HttpOnly cookie is owned by the server; we no longer write it
  // from JS. The login handler returns the JWT in the response body,
  // we store it in localStorage, and api() attaches it as a Bearer.
  // For the WS log-tail handshake the cookie is still set server-side
  // when the JWT arrives via OAuth — there's no equivalent for local
  // login, but logs_ws also accepts the bearer in Sec-WebSocket-Protocol
  // (see extractWSBearer in logs_ws.go).
}

export function clearJwt() {
  if (typeof window === "undefined") return;
  window.localStorage.removeItem(JWT_KEY);
  // Best-effort cookie clear. The HttpOnly attribute means we can
  // overwrite but not read; the browser still sends the empty value
  // until expiry. Server-side, /api/auth/logout (TODO) should clear
  // the cookie authoritatively.
  document.cookie = "kuso.JWT_TOKEN=; path=/; max-age=0; SameSite=Lax; Secure";
}

type Options = Omit<RequestInit, "body"> & { body?: unknown };

export async function api<T>(path: string, opts: Options = {}): Promise<T> {
  const headers = new Headers(opts.headers);
  const jwt = getJwt();
  if (jwt) headers.set("Authorization", `Bearer ${jwt}`);
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
    credentials: "include",
  });
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
