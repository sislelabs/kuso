import { ApiError } from "./api-client";

export type ErrorKind = "not-found" | "forbidden" | "unauthorized" | "client" | "server" | "network" | "unknown";

// classifyError decides what a failed load can honestly offer. Only
// server/network failures are worth a Retry; a 404 or 403 will fail the
// same way every time, so those get a way out instead.
export function classifyError(error: unknown): ErrorKind {
  if (error instanceof ApiError) {
    if (error.status === 404 || error.code === "not_found") return "not-found";
    if (error.status === 403) return "forbidden";
    if (error.status === 401) return "unauthorized";
    if (error.status >= 500) return "server";
    if (error.status >= 400) return "client";
    return "unknown";
  }
  if (error instanceof TypeError) return "network"; // fetch() rejects with TypeError when offline
  return "unknown";
}

export function isRetryable(error: unknown): boolean {
  const k = classifyError(error);
  return k === "server" || k === "network" || k === "unknown";
}

export function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "string" && error) return error;
  return "Unknown error";
}
