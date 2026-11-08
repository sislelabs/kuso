import { ApiError } from "./api-client";
import { toast } from "./toast";

const NETWORK = "Couldn't reach the kuso server. Check your connection and try again.";

// Bare status words the server emits when it has nothing better to say
// ("unauthorized", "internal"). They add nothing over our own sentence.
const BARE = new Set([
  "unauthorized",
  "forbidden",
  "not found",
  "conflict",
  "bad request",
  "internal",
  "internal error",
  "internal server error",
  "service unavailable",
  "bad gateway",
  "gateway timeout",
  "too many requests",
  "request entity too large",
  "unprocessable entity",
]);

// Fragments that mark a message as plumbing (Go/kube/transport internals)
// rather than something a person can act on.
const TECHNICAL = [
  /^\d{3}\b/, // "500 Internal Server Error" — the statusText fallback
  /<\/?[a-z][\s\S]*>/i, // an HTML error page from a proxy
  /\b(json|yaml): /i,
  /unexpected end of json/i,
  /cannot unmarshal/i,
  /invalid character/i,
  /context deadline exceeded/i,
  /context canceled/i,
  /\bdial tcp\b/i,
  /connection (refused|reset)/i,
  /\bEOF\b/,
  /rpc error/i,
  /\bpanic\b/i,
  /goroutine \d+/i,
  /\bsql: /i,
  /\bpq: /i,
  /SQLSTATE/i,
  /\bnil pointer\b/i,
];

// isHumanMessage decides whether the server's own message is worth
// showing verbatim. Conflict and validation messages usually are
// ("addon demo/db already exists"); transport and decode errors aren't.
export function isHumanMessage(msg: string): boolean {
  const m = msg.trim();
  if (m === "" || m.length > 300) return false;
  if (BARE.has(m.toLowerCase().replace(/[.!]+$/, ""))) return false;
  return !TECHNICAL.some((re) => re.test(m));
}

function sentence(msg: string): string {
  const m = msg.trim();
  const capped = m.charAt(0).toUpperCase() + m.slice(1);
  return /[.!?]$/.test(capped) ? capped : `${capped}.`;
}

function isNetworkError(e: unknown): boolean {
  // fetch() rejects with a TypeError ("Failed to fetch", "Load failed",
  // "NetworkError when attempting to fetch resource") when the server is
  // unreachable; that's the only TypeError api() can surface.
  return e instanceof TypeError;
}

function isAbort(e: unknown): boolean {
  return e instanceof Error && (e.name === "AbortError" || e.name === "TimeoutError");
}

function forStatus(status: number): string | null {
  if (status === 401) return "Your session has expired. Sign in again to continue.";
  if (status === 403) return "You don't have permission to do that. Ask an admin to grant your role access.";
  if (status === 404) return "That no longer exists. It may have been deleted or renamed; refresh and try again.";
  if (status === 409) return "Someone else changed this at the same time. Refresh to get the latest version and try again.";
  if (status === 413) return "That's too large for the server to accept. Try a smaller file or fewer items.";
  if (status === 422 || status === 400) return "Some of the values weren't accepted. Check them and try again.";
  if (status === 429) return "Too many requests in a short time. Wait a few seconds and try again.";
  if (status === 502 || status === 503 || status === 504)
    return "The kuso server isn't responding right now. It may be restarting; try again in a moment.";
  if (status >= 500) return "The kuso server hit an unexpected error. Try again; if it keeps happening, check the server logs.";
  return null;
}

// describeError turns anything thrown by api() (or elsewhere) into one
// sentence a person can act on. `fallback` is used when nothing more
// specific is known.
export function describeError(e: unknown, fallback: string): string {
  if (isNetworkError(e)) return NETWORK;
  if (isAbort(e)) return "The request took too long and was cancelled. Try again.";
  if (e instanceof ApiError) {
    const human = isHumanMessage(e.message) ? sentence(e.message) : null;
    // 401 and 5xx: the generic sentence is more useful than whatever the
    // server put there ("unauthorized", "kube: patch …"), except a short
    // human 5xx message, which we keep alongside.
    if (e.status === 401) return forStatus(401) ?? fallback;
    if (e.status >= 500) {
      const base = forStatus(e.status) ?? fallback;
      return human && e.status === 500 ? `${base} (${human.replace(/\.$/, "")})` : base;
    }
    if (human) {
      if (e.status === 403) return `You don't have permission to do that: ${human.charAt(0).toLowerCase()}${human.slice(1)}`;
      return human;
    }
    return forStatus(e.status) ?? fallback;
  }
  if (e instanceof Error && isHumanMessage(e.message)) return sentence(e.message);
  if (typeof e === "string" && isHumanMessage(e)) return sentence(e);
  return fallback;
}

export interface ToastErrorOptions {
  retry?: () => void;
  id?: string | number;
}

// toastError: `fallback` is the headline ("Couldn't save the alert rule"),
// describeError the detail. Retry is offered only for failures that can
// plausibly succeed on a second try.
export function toastError(e: unknown, fallback: string, opts: ToastErrorOptions = {}) {
  const detail = describeError(e, fallback);
  const retryable =
    isNetworkError(e) || isAbort(e) || (e instanceof ApiError && (e.status >= 500 || e.status === 429 || e.status === 409));
  return toast.error(fallback, {
    id: opts.id,
    description: detail === fallback ? undefined : detail,
    action: opts.retry && retryable ? { label: "Retry", onClick: opts.retry } : undefined,
  });
}
