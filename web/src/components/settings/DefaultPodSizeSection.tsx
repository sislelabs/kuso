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
import { SettingsField, SettingsSection, SettingsSelect } from "./SettingsSection";

function describe(p: PodSize): string {
  const req = [p.CPURequest, p.MemoryRequest].filter(Boolean).join(" / ");
  const lim = [p.CPULimit, p.MemoryLimit].filter(Boolean).join(" / ");
  return `Requests ${req || "none"} · limits ${lim || "none"}`;
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
    <SettingsSection
      title="Default pod size"
      description="CPU and memory requests/limits for new services that don't set their own. Existing services keep what they have."
      footerHint="Saves as soon as you pick a preset."
    >
      <SettingsField label="Preset" htmlFor="default-podsize" hint={preset ? describe(preset) : undefined}>
        <SettingsSelect
          id="default-podsize"
          value={selected}
          onChange={(e) => save.mutate(e.target.value)}
          disabled={!canEdit || save.isPending || current.isPending || sizes.isPending}
        >
          {(sizes.data ?? []).map((p) => (
            <option key={p.ID} value={p.Name}>
              {p.Name}
            </option>
          ))}
          {selected && selected !== POD_SIZE_NONE && !preset && !sizes.isPending && (
            <option value={selected}>{selected} (preset deleted)</option>
          )}
          <option value={POD_SIZE_NONE}>None (no requests or limits)</option>
        </SettingsSelect>
      </SettingsField>
    </SettingsSection>
  );
}
