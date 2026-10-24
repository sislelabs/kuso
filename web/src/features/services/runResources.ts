export interface RunResourceFields {
  cpuRequest: string;
  cpuLimit: string;
  memRequest: string;
  memLimit: string;
}

// k8s quantity: plain/decimal number with an optional SI or binary suffix.
const QUANTITY_RE = /^[0-9]+(\.[0-9]+)?(m|k|M|G|T|P|E|Ki|Mi|Gi|Ti|Pi|Ei)?$/;

// runResources maps the run composer's quantity fields onto a k8s
// ResourceRequirements block; all-blank = undefined (chart default).
export function runResources(
  f: RunResourceFields,
): { resources?: { requests?: Record<string, string>; limits?: Record<string, string> } } | { error: string } {
  const requests: Record<string, string> = {};
  const limits: Record<string, string> = {};
  const fields: [string, string, Record<string, string>, string][] = [
    ["CPU request", f.cpuRequest, requests, "cpu"],
    ["CPU limit", f.cpuLimit, limits, "cpu"],
    ["memory request", f.memRequest, requests, "memory"],
    ["memory limit", f.memLimit, limits, "memory"],
  ];
  for (const [label, raw, into, key] of fields) {
    const v = raw.trim();
    if (!v) continue;
    if (!QUANTITY_RE.test(v)) return { error: `${label} "${v}" isn't a quantity (e.g. 500m, 1, 512Mi, 2Gi)` };
    into[key] = v;
  }
  const out: { requests?: Record<string, string>; limits?: Record<string, string> } = {};
  if (Object.keys(requests).length) out.requests = requests;
  if (Object.keys(limits).length) out.limits = limits;
  return Object.keys(out).length ? { resources: out } : {};
}
