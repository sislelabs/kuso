"use client";

import { HardDrive, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useStorageClasses } from "@/features/services";
import { cn } from "@/lib/utils";
import { Section, Field, Segmented, Callout, monoInputClass, type SectionProps, type VolumeRow } from "./_primitives";

const ACCESS_MODES = [
  { value: "", label: "Single node (RWO)" },
  { value: "ReadWriteMany", label: "Shared (RWX)" },
] as const;

// existingVolumes is the saved list. The server keys volumes by name and
// storageClass/accessMode are immutable on a bound PVC, so they're only
// editable on rows whose name isn't already a saved volume.
export function VolumesSection({
  state,
  setState,
  existingVolumes,
}: SectionProps & { existingVolumes: VolumeRow[] }) {
  const hasNew = state.volumes.some((v) => !existingVolumes.some((e) => e.name === v.name));
  const classes = useStorageClasses(hasNew);
  const add = () =>
    setState((s) => ({
      ...s,
      volumes: [...s.volumes, { name: "", mountPath: "", sizeGi: 1 }],
    }));
  const update = (i: number, patch: Partial<VolumeRow>) =>
    setState((s) => ({
      ...s,
      volumes: s.volumes.map((v, j) => (j === i ? { ...v, ...patch } : v)),
    }));
  const remove = (i: number) =>
    setState((s) => ({ ...s, volumes: s.volumes.filter((_, j) => j !== i) }));

  const removedSaved = existingVolumes.filter((e) => !state.volumes.some((v) => v.name === e.name));

  return (
    <Section
      id="volumes"
      title="Volumes"
      icon={HardDrive}
      status={state.volumes.length === 0 ? "None" : `${state.volumes.length} volume${state.volumes.length === 1 ? "" : "s"}`}
      description="Persistent disks mounted into every pod. Use them for SQLite, uploads or anything that must survive a restart."
      footerNote="Adding or resizing a volume redeploys the service."
    >
      {state.volumes.length === 0 ? (
        <p className="text-xs text-[var(--text-tertiary)]">No volumes. Pods start with an empty filesystem on every deploy.</p>
      ) : (
        <div className="space-y-3">
          {state.volumes.map((v, i) => {
            const saved = existingVolumes.some((e) => e.name === v.name);
            return (
              <div key={i} className="space-y-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]/40 p-3">
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)_96px_32px] sm:items-end">
                  <Field label="Name" htmlFor={`vol-name-${i}`}>
                    <Input
                      id={`vol-name-${i}`}
                      value={v.name}
                      onChange={(e) => update(i, { name: e.target.value })}
                      placeholder="e.g. data"
                      className={monoInputClass}
                      spellCheck={false}
                    />
                  </Field>
                  <Field label="Mount path" htmlFor={`vol-path-${i}`}>
                    <Input
                      id={`vol-path-${i}`}
                      value={v.mountPath}
                      onChange={(e) => update(i, { mountPath: e.target.value })}
                      placeholder="e.g. /var/lib/app"
                      className={monoInputClass}
                      spellCheck={false}
                    />
                  </Field>
                  <Field label="Size" htmlFor={`vol-size-${i}`}>
                    <div className="relative">
                      <Input
                        id={`vol-size-${i}`}
                        type="number"
                        inputMode="numeric"
                        value={v.sizeGi}
                        onChange={(e) => update(i, { sizeGi: Math.max(1, Number(e.target.value) || 1) })}
                        min={1}
                        className={cn(monoInputClass, "pr-8")}
                      />
                      <span className="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 text-xs text-[var(--text-tertiary)]">
                        GiB
                      </span>
                    </div>
                  </Field>
                  <button
                    type="button"
                    onClick={() => remove(i)}
                    aria-label={`Remove volume ${v.name || i + 1}`}
                    className="inline-flex h-8 w-8 items-center justify-center justify-self-end rounded-sm text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--error)]"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                </div>
                {!saved ? (
                  <NewVolumeOptions
                    volume={v}
                    classes={classes.isError ? null : (classes.data ?? [])}
                    onChange={(patch) => update(i, patch)}
                  />
                ) : (
                  (v.accessMode || v.storageClass) && (
                    <p className="text-xs text-[var(--text-secondary)]">
                      {[v.accessMode === "ReadWriteMany" ? "Shared (RWX)" : v.accessMode, v.storageClass && `storage class ${v.storageClass}`]
                        .filter(Boolean)
                        .join(" · ")}
                      . Fixed when the volume was created.
                    </p>
                  )
                )}
              </div>
            );
          })}
        </div>
      )}
      {removedSaved.length > 0 && (
        <Callout tone="warning">
          Removing <span className="font-mono">{removedSaved.map((r) => r.name).join(", ")}</span> unmounts it. The disk
          and its data are kept but nothing mounts it any more.
        </Callout>
      )}
      <Button type="button" variant="outline" size="xs" onClick={add}>
        <Plus className="h-3 w-3" />
        Add volume
      </Button>
    </Section>
  );
}

function NewVolumeOptions({
  volume,
  classes,
  onChange,
}: {
  volume: VolumeRow;
  // null = the list isn't readable (non-admin, or RBAC missing): fall
  // back to a free-text class name.
  classes: { name: string; isDefault: boolean }[] | null;
  onChange: (patch: Partial<VolumeRow>) => void;
}) {
  const sc = volume.storageClass ?? "";
  const options = classes
    ? [{ value: "", label: "cluster default" }, ...classes.map((c) => ({ value: c.name, label: c.isDefault ? `${c.name} (default)` : c.name }))]
    : null;
  if (options && sc && !options.some((o) => o.value === sc)) options.push({ value: sc, label: `${sc} (missing)` });
  const setClass = (value: string) => onChange({ storageClass: value || undefined });
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Field label="Storage class">
          {options ? (
            <Segmented options={options} value={sc} onChange={setClass} label="Storage class" />
          ) : (
            <Input
              value={sc}
              onChange={(e) => setClass(e.target.value.trim())}
              placeholder="cluster default"
              aria-label="Storage class"
              className={monoInputClass}
              spellCheck={false}
            />
          )}
        </Field>
        <Field label="Access">
          <Segmented
            options={ACCESS_MODES}
            value={volume.accessMode ?? ""}
            onChange={(value) => onChange({ accessMode: value || undefined })}
            label="Access mode"
          />
        </Field>
      </div>
      <p className="text-xs text-[var(--text-secondary)]">
        Both are fixed once the volume exists. Shared (RWX) needs a storage class that supports it, e.g. longhorn or nfs.
      </p>
    </div>
  );
}
