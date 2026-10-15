"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { getDefaultPodSize } from "@/features/cluster-config/api";
import {
  createPodSize,
  defaultPodSizeQueryKey,
  deletePodSize,
  listPodSizeRows,
  podSizeToInput,
  podSizesQueryKey,
  updatePodSize,
  validatePodSize,
  type PodSizeInput,
  type PodSizeRow,
} from "@/features/instance-config";

const EMPTY: PodSizeInput = {
  Name: "",
  CPURequest: "",
  CPULimit: "",
  MemoryRequest: "",
  MemoryLimit: "",
  Description: "",
};

const labelCls = "font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]";

// editing: null = closed, "new" = create form, otherwise the preset's ID.
type Editing = null | "new" | string;

// PodSizePresetsSection manages the named CPU/memory presets offered to
// services. Mutations need settings:admin.
export function PodSizePresetsSection({ canEdit }: { canEdit: boolean }) {
  const qc = useQueryClient();
  // Same key and endpoint as DefaultPodSizeSection, so its picker
  // refreshes when a preset changes here.
  const sizes = useQuery({ queryKey: podSizesQueryKey, queryFn: listPodSizeRows });
  const current = useQuery({ queryKey: defaultPodSizeQueryKey, queryFn: getDefaultPodSize });
  const [editing, setEditing] = useState<Editing>(null);
  const [form, setForm] = useState<PodSizeInput>(EMPTY);
  const [formError, setFormError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<PodSizeRow | null>(null);

  const invalidate = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: podSizesQueryKey }),
      qc.invalidateQueries({ queryKey: defaultPodSizeQueryKey }),
    ]);

  const save = useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: (v: { id: string | null; input: PodSizeInput }) =>
      v.id ? updatePodSize(v.id, v.input) : createPodSize(v.input),
    onSuccess: (p, v) => {
      toast.success(v.id ? `Preset ${p.Name} updated` : `Preset ${p.Name} created`);
      setEditing(null);
      void invalidate();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });

  const remove = useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: (id: string) => deletePodSize(id),
    onSuccess: () => {
      toast.success("Preset deleted");
      void invalidate();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
    onSettled: () => setDeleting(null),
  });

  const open = (target: Editing, initial: PodSizeInput) => {
    setEditing(target);
    setForm(initial);
    setFormError(null);
  };

  const editingRow = editing && editing !== "new" ? sizes.data?.find((p) => p.ID === editing) : undefined;
  const isDefault = (p: PodSizeRow) => current.data === p.Name;

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const others = (sizes.data ?? []).filter((p) => p.ID !== editing).map((p) => p.Name);
    const err = validatePodSize(form, others);
    setFormError(err);
    if (err) return;
    save.mutate({ id: editing === "new" ? null : editing, input: form });
  };

  const field = (k: keyof PodSizeInput, label: string, placeholder: string) => (
    <div className="space-y-1">
      <label htmlFor={`podsize-${k}`} className={labelCls}>{label}</label>
      <Input
        id={`podsize-${k}`}
        value={form[k]}
        onChange={(e) => setForm((f) => ({ ...f, [k]: e.target.value }))}
        placeholder={placeholder}
        className="h-8 font-mono text-[12px]"
        spellCheck={false}
        autoComplete="off"
      />
    </div>
  );

  const editor = (
    <form onSubmit={onSubmit} className="space-y-3 bg-[var(--bg-primary)] p-4">
      <div className="grid gap-3 sm:grid-cols-2">
        {field("Name", "name", "medium")}
        {field("Description", "description", "default for most web apps")}
        {field("CPURequest", "cpu request", "100m")}
        {field("CPULimit", "cpu limit (blank = none)", "1")}
        {field("MemoryRequest", "memory request", "256Mi")}
        {field("MemoryLimit", "memory limit", "1Gi")}
      </div>
      {editingRow && isDefault(editingRow) && form.Name.trim() !== editingRow.Name && (
        <p className="font-mono text-[10px] text-[var(--warning)]">
          This is the default preset. The default is stored by name, so renaming it leaves new
          services with no requests or limits until you re-pick the default below.
        </p>
      )}
      {formError && <p className="font-mono text-[10px] text-[var(--error)]">{formError}</p>}
      <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
        Services already using this preset keep the resources they were given.
      </p>
      <div className="flex justify-end gap-2">
        <Button size="sm" variant="ghost" type="button" onClick={() => setEditing(null)}>
          Cancel
        </Button>
        <Button size="sm" type="submit" disabled={save.isPending}>
          {save.isPending ? "Saving…" : editing === "new" ? "Create preset" : "Save preset"}
        </Button>
      </div>
    </form>
  );

  const rows = sizes.data ?? [];

  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="flex items-start justify-between gap-3 border-b border-[var(--border-subtle)] px-4 py-2.5">
        <div>
          <h2 className="text-sm font-semibold tracking-tight">Pod size presets</h2>
          <p className="mt-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
            Named CPU/memory requests and limits services can pick (kuso service size).
          </p>
        </div>
        {canEdit && editing !== "new" && (
          <Button size="sm" variant="outline" onClick={() => open("new", EMPTY)}>
            <Plus className="h-3 w-3" /> Add preset
          </Button>
        )}
      </header>

      {editing === "new" && <div className="border-b border-[var(--border-subtle)]">{editor}</div>}

      {sizes.isPending ? (
        <Skeleton className="m-3 h-16" />
      ) : sizes.isError ? (
        <QueryErrorState what="pod size presets" error={sizes.error} onRetry={() => void sizes.refetch()} className="m-3" />
      ) : rows.length === 0 ? (
        <p className="px-4 py-4 text-[11px] text-[var(--text-tertiary)]">
          No presets. kuso re-seeds small / medium / large on the next server start when the
          table is empty.
        </p>
      ) : (
        <ul>
          {rows.map((p) => (
            <li key={p.ID} className="border-b border-[var(--border-subtle)] last:border-b-0">
              <div className="flex items-center gap-3 px-4 py-2.5">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-[12px] text-[var(--text-primary)]">{p.Name}</span>
                    {isDefault(p) && (
                      <span className="rounded bg-[var(--accent-subtle)] px-1 font-mono text-[9px] uppercase tracking-widest text-[var(--text-secondary)]">
                        default
                      </span>
                    )}
                    {p.Description?.Valid && (
                      <span className="truncate text-[11px] text-[var(--text-tertiary)]">{p.Description.String}</span>
                    )}
                  </div>
                  <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
                    req {p.CPURequest || "-"} / {p.MemoryRequest || "-"} · limit {p.CPULimit || "-"} /{" "}
                    {p.MemoryLimit || "-"}
                  </p>
                </div>
                {canEdit && (
                  <>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Edit ${p.Name}`}
                      onClick={() => open(p.ID, podSizeToInput(p))}
                      disabled={editing === p.ID}
                    >
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Delete ${p.Name}`}
                      onClick={() => setDeleting(p)}
                      disabled={remove.isPending}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </>
                )}
              </div>
              {editing === p.ID && <div className="border-t border-[var(--border-subtle)]">{editor}</div>}
            </li>
          ))}
        </ul>
      )}

      <ConfirmDialog
        open={deleting !== null}
        title={`Delete preset ${deleting?.Name ?? ""}?`}
        body={
          <div className="space-y-2">
            <p>Services already sized with it keep their resources; it just stops being offered.</p>
            {deleting && isDefault(deleting) && (
              <p className="text-[var(--warning)]">
                This is the default preset. Until you pick another default, new services get no
                CPU/memory requests or limits.
              </p>
            )}
          </div>
        }
        confirmLabel="Delete preset"
        destructive
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.ID)}
        onCancel={() => setDeleting(null)}
      />
    </section>
  );
}
