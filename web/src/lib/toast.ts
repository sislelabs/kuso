// kuso-toast: a thin wrapper over sonner that enforces sane
// per-severity durations.
//
// Why: sonner's defaults dismiss every toast at 4s. That's fine for
// "saved" but a footgun for "build failed: kaniko OOMKilled" — the
// user blinks and the only signal of the failure is gone. We standardise:
//
//   success: 3s   — quick confirm, dismissable
//   info:    5s   — slightly more time to read
//   warning: 10s  — recoverable issue, give it dwell
//   error:   30s  — failure path, likely needs action; user dismissable
//
// Anywhere in the codebase that imports `toast` from "sonner" still
// works (back-compat), but prefer `kt` from this file so consistency
// stays cheap to maintain.

import { toast as sonnerToast, type ExternalToast } from "sonner";

const DEFAULTS = {
  success: 3_000,
  info: 5_000,
  warning: 10_000,
  error: 30_000,
} as const;

type Opts = ExternalToast | undefined;

function withDefault(opts: Opts, fallback: number): ExternalToast {
  return { duration: fallback, ...(opts ?? {}) };
}

export const kt = {
  success: (msg: string, opts?: Opts) =>
    sonnerToast.success(msg, withDefault(opts, DEFAULTS.success)),
  info: (msg: string, opts?: Opts) =>
    sonnerToast.info?.(msg, withDefault(opts, DEFAULTS.info)) ??
    sonnerToast(msg, withDefault(opts, DEFAULTS.info)),
  warning: (msg: string, opts?: Opts) =>
    sonnerToast.warning?.(msg, withDefault(opts, DEFAULTS.warning)) ??
    sonnerToast(msg, withDefault(opts, DEFAULTS.warning)),
  error: (msg: string, opts?: Opts) =>
    sonnerToast.error(msg, withDefault(opts, DEFAULTS.error)),
  message: (msg: string, opts?: Opts) =>
    sonnerToast.message(msg, withDefault(opts, DEFAULTS.info)),
  promise: sonnerToast.promise,
  dismiss: sonnerToast.dismiss,
};

export { kt as toast };
