import type { HealthcheckBody } from "@/features/services";
import type { FormState } from "./_primitives";

// Chart/CRD defaults, used for placeholders and the "what this means" line.
export const HC_DEFAULTS = { delay: 5, period: 10, timeout: 2, threshold: 3 } as const;

const NUM_FIELDS = [
  ["hcDelay", "initialDelaySeconds", "Initial delay", 3600],
  ["hcPeriod", "periodSeconds", "Check interval", 300],
  ["hcTimeout", "timeoutSeconds", "Timeout", 60],
  ["hcThreshold", "failureThreshold", "Failure threshold", 30],
] as const;

const HC_FIELDS = ["hcEnabled", "hcPath", "hcPort", "hcDelay", "hcPeriod", "hcTimeout", "hcThreshold"] as const;

// healthcheckPatch mirrors the server's normalizeHealthcheck so a bad value
// fails in the form, not as a 400. The server replaces the block wholesale,
// so any change sends the whole check; turning it off sends an empty path.
export function healthcheckPatch(
  state: FormState,
  base: FormState,
): { healthcheck?: HealthcheckBody } | { error: string } {
  if (HC_FIELDS.every((f) => state[f] === base[f])) return {};
  if (!state.hcEnabled) {
    return base.hcEnabled ? { healthcheck: { path: "" } } : {};
  }
  const path = state.hcPath.trim();
  if (!path) return { error: "Health check path is required, e.g. /health" };
  if (!path.startsWith("/") || path.startsWith("//") || /\s|\\/.test(path) || path.length > 512) {
    return { error: "Health check path must start with a single / and contain no spaces" };
  }
  const out: HealthcheckBody = { path };
  if (state.hcPort.trim()) {
    const port = Number(state.hcPort);
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      return { error: "Health check port must be between 1 and 65535" };
    }
    out.port = port;
  }
  for (const [field, key, label, max] of NUM_FIELDS) {
    const raw = state[field].trim();
    if (!raw) continue;
    const n = Number(raw);
    if (!Number.isInteger(n) || n < 1 || n > max) {
      return { error: `${label} must be a whole number between 1 and ${max}` };
    }
    out[key] = n;
  }
  const period = out.periodSeconds ?? HC_DEFAULTS.period;
  const timeout = out.timeoutSeconds ?? HC_DEFAULTS.timeout;
  if (timeout > period) return { error: "Timeout can't be longer than the check interval" };
  return { healthcheck: out };
}

// secondsUntilRestart is how long a pod that stops answering keeps getting
// traffic before it's pulled and restarted.
export function secondsUntilRestart(state: FormState): number {
  const n = (v: string, d: number) => {
    const x = Number(v);
    return v.trim() && Number.isFinite(x) && x > 0 ? x : d;
  };
  return n(state.hcPeriod, HC_DEFAULTS.period) * n(state.hcThreshold, HC_DEFAULTS.threshold);
}

export function describeHealthcheck(s: FormState): string {
  if (!s.hcEnabled) return "TCP port check";
  const path = s.hcPath.trim() || "(no path)";
  const port = s.hcPort.trim() ? `:${s.hcPort.trim()}` : "";
  const timing = [
    s.hcDelay.trim() && `delay ${s.hcDelay.trim()}s`,
    s.hcPeriod.trim() && `every ${s.hcPeriod.trim()}s`,
    s.hcTimeout.trim() && `timeout ${s.hcTimeout.trim()}s`,
    s.hcThreshold.trim() && `${s.hcThreshold.trim()} failures`,
  ]
    .filter(Boolean)
    .join(", ");
  return `GET ${path}${port}${timing ? ` (${timing})` : ""}`;
}
