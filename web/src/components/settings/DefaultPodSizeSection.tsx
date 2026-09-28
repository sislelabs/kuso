"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  POD_SIZE_NONE,
  getDefaultPodSize,
  listPodSizes,
  setDefaultPodSize,
  type PodSize,
} from "@/features/cluster-config/api";

function describe(p: PodSize): string {
  const req = [p.CPURequest, p.MemoryRequest].filter(Boolean).join(" / ");
  const lim = [p.CPULimit, p.MemoryLimit].filter(Boolean).join(" / ");
  return `req ${req || "-"} · limit ${lim || "-"}`;
}

// Picks the preset a service created without explicit resources gets.
// Existing services keep what they have.
export function DefaultPodSizeSection({ canEdit }: { canEdit: boolean }) {
  const qc = useQueryClient();
  const sizes = useQuery({ queryKey: ["admin", "podsizes"], queryFn: listPodSizes });
  const current = useQuery({ queryKey: ["admin", "default-podsize"], queryFn: getDefaultPodSize });
  const save = useMutation({
    mutationFn: setDefaultPodSize,
    onSuccess: (_, name) => {
      toast.success(`Default pod size set to ${name}`);
      qc.invalidateQueries({ queryKey: ["admin", "default-podsize"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });

  const selected = current.data ?? "";
  const preset = sizes.data?.find((p) => p.Name === selected);

  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="border-b border-[var(--border-subtle)] px-4 py-2.5">
        <h2 className="text-sm font-semibold tracking-tight">Default pod size</h2>
        <p className="mt-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
          CPU/memory requests and limits given to new services that don&apos;t set their own.
          Existing services are not changed.
        </p>
      </header>
      <div className="grid grid-cols-[140px_1fr] items-center gap-3 p-4">
        <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          Preset
        </div>
        <div className="space-y-1">
          <select
            value={selected}
            onChange={(e) => save.mutate(e.target.value)}
            disabled={!canEdit || save.isPending || current.isPending || sizes.isPending}
            className="h-8 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)] disabled:opacity-50"
          >
            {(sizes.data ?? []).map((p) => (
              <option key={p.ID} value={p.Name}>
                {p.Name}
              </option>
            ))}
            {selected && selected !== POD_SIZE_NONE && !preset && !sizes.isPending && (
              <option value={selected}>{selected} (preset deleted)</option>
            )}
            <option value={POD_SIZE_NONE}>none (no requests or limits)</option>
          </select>
          {preset && (
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">{describe(preset)}</p>
          )}
        </div>
      </div>
    </section>
  );
}
