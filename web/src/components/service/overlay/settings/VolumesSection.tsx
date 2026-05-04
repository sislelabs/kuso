"use client";

import { HardDrive, Plus, X } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Section, type SectionProps, type VolumeRow } from "./_primitives";

export function VolumesSection({ state, setState }: SectionProps) {
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
            className="grid grid-cols-[120px_1fr_72px_28px] items-center gap-1.5 border-b border-[var(--border-subtle)] px-3 py-1.5 last:border-b-0"
          >
            <Input
              value={v.name}
              onChange={(e) => update(i, { name: e.target.value })}
              placeholder="data"
              className="h-7 font-mono text-[11px]"
            />
            <Input
              value={v.mountPath}
              onChange={(e) => update(i, { mountPath: e.target.value })}
              placeholder="/var/lib/app"
              className="h-7 font-mono text-[11px]"
            />
            <div className="relative">
              <Input
                type="number"
                value={v.sizeGi}
                onChange={(e) =>
                  update(i, { sizeGi: Math.max(1, Number(e.target.value) || 1) })
                }
                min={1}
                className="h-7 pr-6 font-mono text-[11px]"
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
