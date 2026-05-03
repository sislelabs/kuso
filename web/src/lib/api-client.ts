import { env } from "./env";

const JWT_KEY = "kuso.jwt";

export class ApiError extends Error {
  status: number;
  body: unknown;
  constructor(status: number, body: unknown, message: string) {
    super(message);
    this.status = status;
    this.body = body;
  }
}

export function getJwt(): string | null {
  if (typeof window === "undefined") return null;
  // Cookie wins over localStorage. Order matters because the post-
  // OAuth handoff drops a fresh kuso.JWT_TOKEN cookie via
  // setJWTCookie on the server, then 302s back to "/". If a stale
  // localStorage entry was preferred, subsequent /api requests would
  // send the old (often expired) bearer and 401, leaving the user
  // stuck on the landing page even though OAuth succeeded. Reading
  // the cookie first keeps the freshest token in flight.
  const m = document.cookie.match(/(?:^|; )kuso\.JWT_TOKEN=([^;]+)/);
  if (m && m[1]) {
    const token = decodeURIComponent(m[1]);
    // Mirror to localStorage so api() can fall back to it if the
    // cookie is later cleared (e.g. session expiry on the server).
    window.localStorage.setItem(JWT_KEY, token);
    return token;
  }
  return window.localStorage.getItem(JWT_KEY);
}

export function setJwt(token: string) {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(JWT_KEY, token);
  // Also set the cookie kuso-server's middleware reads on browser-driven
  // endpoints. Path / and SameSite=Lax keeps OAuth redirects working.
  document.cookie = `kuso.JWT_TOKEN=${token}; path=/; SameSite=Lax`;
}

export function clearJwt() {
  if (typeof window === "undefined") return;
  window.localStorage.removeItem(JWT_KEY);
  document.cookie = "kuso.JWT_TOKEN=; path=/; max-age=0; SameSite=Lax";
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
