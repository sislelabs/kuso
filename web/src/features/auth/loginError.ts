import { ApiError } from "@/lib/api-client";

export function loginErrorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 401) return "invalid credentials";
    if (e.status === 429) return "too many attempts, wait a minute and try again";
    if (e.status >= 500) return `server error (${e.status}), try again shortly`;
    return e.message || `login failed (${e.status})`;
  }
  // fetch rejects with a TypeError when the server is unreachable.
  if (e instanceof TypeError) return "couldn't reach the server";
  return "login failed";
}
