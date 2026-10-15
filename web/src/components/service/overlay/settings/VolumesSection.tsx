"use client";

import { HardDrive, Plus, X } from "lucide-react";
import { Input } from "@/components/ui/input";
import { useStorageClasses } from "@/features/services";
import { cn } from "@/lib/utils";
import { Section, type SectionProps, type VolumeRow } from "./_primitives";

const ACCESS_MODES = [
  { value: "", label: "RWO (default)" },
  { value: "ReadWriteMany", label: "RWX" },
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

  return (
    <Section
      id="volumes"
      title="Volumes"
      icon={HardDrive}
      hint={state.volumes.length === 0 ? "none" : `${state.volumes.length}`}
    >
      {state.volumes.length === 0 ? (
        <p className="px-3 py-2.5 text-[11px] text-[var(--text-tertiary)]">
          No persistent volumes. Add one for SQLite, file uploads, or any state that
          should survive pod restarts.
        </p>
      ) : (
        state.volumes.map((v, i) => (
          <div
            key={i}
            className="grid grid-cols-[120px_minmax(0,1fr)_72px_28px] items-center gap-1.5 border-b border-[var(--border-subtle)] px-3 py-1.5 last:border-b-0"
          >
            <Input
              value={v.name}
              onChange={(e) => update(i, { name: e.target.value })}
              placeholder="data"
              className="h-7 min-w-0 font-mono text-[11px]"
            />
            <Input
              value={v.mountPath}
              onChange={(e) => update(i, { mountPath: e.target.value })}
              placeholder="/var/lib/app"
              className="h-7 min-w-0 font-mono text-[11px]"
            />
            <div className="relative min-w-0">
              <Input
                type="number"
                value={v.sizeGi}
                onChange={(e) =>
                  update(i, { sizeGi: Math.max(1, Number(e.target.value) || 1) })
                }
                min={1}
                className="h-7 min-w-0 pr-6 font-mono text-[11px]"
              />
              <span className="pointer-events-none absolute right-1.5 top-1/2 -translate-y-1/2 font-mono text-[10px] text-[var(--text-tertiary)]">
                Gi
              </span>
            </div>
            <button
              type="button"
              onClick={() => remove(i)}
              aria-label="Remove volume"
              className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
            >
              <X className="h-3 w-3" />
            </button>
            {!existingVolumes.some((e) => e.name === v.name) ? (
              <NewVolumeOptions
                volume={v}
                classes={classes.isError ? null : (classes.data ?? [])}
                onChange={(patch) => update(i, patch)}
              />
            ) : (v.accessMode || v.storageClass) && (
              <p
                className="col-span-4 font-mono text-[10px] text-[var(--text-tertiary)]"
                title="Fixed when the volume was created; immutable on a bound PVC"
              >
                {[v.accessMode, v.storageClass && `class ${v.storageClass}`].filter(Boolean).join(" · ")} (read-only)
              </p>
            )}
          </div>
        ))
      )}
      <button
        type="button"
        onClick={add}
        className="flex w-full items-center gap-1.5 border-t border-[var(--border-subtle)] px-3 py-2 text-left text-[11px] text-[var(--accent)] hover:bg-[var(--bg-tertiary)]/40"
      >
        <Plus className="h-3 w-3" />
        add volume
      </button>
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
    <div className="col-span-4 flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[10px] text-[var(--text-tertiary)]">
      <span>class</span>
      {options ? (
        <Pills options={options} value={sc} onChange={setClass} label="Storage class" />
      ) : (
        <Input
          value={sc}
          onChange={(e) => setClass(e.target.value.trim())}
          placeholder="cluster default"
          aria-label="Storage class"
          className="h-6 w-36 font-mono text-[10px]"
          spellCheck={false}
        />
      )}
      <span>access</span>
      <Pills
        options={[...ACCESS_MODES]}
        value={volume.accessMode ?? ""}
        onChange={(value) => onChange({ accessMode: value || undefined })}
        label="Access mode"
      />
      <span className="basis-full">fixed once the volume is created · RWX needs a class that supports it (e.g. longhorn, nfs)</span>
    </div>
  );
}

function Pills({
  options,
  value,
  onChange,
  label,
}: {
  options: { value: string; label: string }[];
  value: string;
  onChange: (v: string) => void;
  label: string;
}) {
  return (
    <div role="group" aria-label={label} className="inline-flex flex-wrap items-center gap-0.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
      {options.map((o) => (
        <button
          key={o.value || "default"}
          type="button"
          aria-pressed={value === o.value}
          onClick={() => onChange(o.value)}
          className={cn(
            "rounded px-1.5 py-0.5 text-[10px] whitespace-nowrap transition-colors",
            value === o.value
              ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
              : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
