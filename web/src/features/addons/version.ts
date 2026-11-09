// addonVersion is the one answer to "which version is this addon": the
// running image's version beats the spec's pin, which beats the chart
// default (unknown → ""). Image-tag flavours are dropped ("16-alpine" → "16").
export function addonVersion(specVersion?: string, runningVersion?: string): string {
  const clean = (v?: string) => (v ?? "").trim().replace(/^v(?=\d)/, "").replace(/^(\d[\w.]*?)-[a-z].*$/i, "$1");
  return clean(runningVersion) || clean(specVersion);
}

// addonVersionLabel: "Postgres 16", or just "Postgres" when nothing says which.
export function addonVersionLabel(kindLabel: string, specVersion?: string, runningVersion?: string): string {
  const v = addonVersion(specVersion, runningVersion);
  return v ? `${kindLabel} ${v}` : kindLabel;
}
