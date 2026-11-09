// addonVersion is the one answer to "which version is this addon": the
// running image's version beats the spec's pin, which beats the chart
// default (unknown → ""). Image-tag flavours are dropped ("16-alpine" → "16").
export function addonVersion(specVersion?: string, runningVersion?: string): string {
  // MinIO tags are RELEASE.2026-08-04T00-00-00Z: the date is the version.
  const clean = (v?: string) => (v ?? "").trim().replace(/^RELEASE\.(\d{4}-\d{2}-\d{2})T.*$/, "$1").replace(/^v(?=\d)/, "").replace(/^(\d[\w.]*?)-[a-z].*$/i, "$1");
  return clean(runningVersion) || clean(specVersion);
}

// addonVersionLabel: "Postgres 16", or just "Postgres" when nothing says which.
export function addonVersionLabel(kindLabel: string, specVersion?: string, runningVersion?: string): string {
  const v = addonVersion(specVersion, runningVersion);
  return v ? `${kindLabel} ${v}` : kindLabel;
}
