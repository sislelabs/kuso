// A variable whose addon key carries no value: either injected from the
// addon's connection secret empty, or a ${{ addon.KEY }} reference that
// resolved to "". Pooler keys are the common case (the pooler is off).
export interface DanglingRef {
  addon: string;
  key: string;
  pooler: boolean;
  // A reference row the user wrote (removable); otherwise injected by the addon.
  isRef: boolean;
}

const REF = /^\$\{\{\s*([A-Za-z0-9_-]+)\.([A-Za-z0-9_]+)\s*\}\}$/;

export function danglingRef(
  row: { name: string; value: string; addon?: string; resolved?: string },
  addonNames: ReadonlySet<string>,
): DanglingRef | null {
  if (row.addon && row.value === "") {
    return { addon: row.addon, key: row.name, pooler: /POOL|BOUNCER/i.test(row.name), isRef: false };
  }
  const m = REF.exec(row.value.trim());
  if (!m || row.resolved !== "" || !addonNames.has(m[1])) return null;
  return { addon: m[1], key: m[2], pooler: /POOL|BOUNCER/i.test(m[2]), isRef: true };
}
