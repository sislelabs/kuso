// trimRepeatedCause drops the part of a server detail that repeats the
// label it sits next to: "Build failed" + "latest build failed and nothing
// is running" reads "Build failed · Nothing is running", not the cause twice.
export function trimRepeatedCause(label: string, detail?: string): string | undefined {
  if (!detail) return detail;
  const norm = (s: string) => s.toLowerCase().replace(/[^a-z0-9 ]/g, "").trim();
  const l = norm(label);
  const d = detail.trim();
  const m = /^(?:the\s+)?(?:latest\s+|last\s+)?(.*)$/i.exec(d);
  const body = m ? m[1] : d;
  if (!l || !norm(body).startsWith(l)) return d;
  const rest = body.slice(label.length).replace(/^[\s,:;·-]*(?:and|but|so)?\s*/i, "").trim();
  if (!rest) return undefined;
  return rest.charAt(0).toUpperCase() + rest.slice(1);
}
