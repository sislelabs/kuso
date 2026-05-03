"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import { toast } from "sonner";
import { Shield, Plus, Trash2 } from "lucide-react";

interface PermissionRow {
  resource: string;
  action: string;
}
interface FullRole {
  id: string;
  name: string;
  description: string;
  permissions: PermissionRow[];
}

interface ListResp {
  data?: FullRole[];
}

// /settings/roles — manages legacy role-permissions matrix. New
// installs should use Groups for tenancy; Roles stays for back-
// compat with users on the role pivot. We surface it but make the
// "use Groups instead" hint loud.
export default function RolesPage() {
  const canWrite = useCan(Perms.UserWrite);
  const list = useQuery({
    queryKey: ["admin", "roles"],
    queryFn: async () => {
      const res = await api<ListResp | FullRole[]>("/api/roles/full");
      return Array.isArray(res) ? res : (res.data ?? []);
    },
  });
  const [creating, setCreating] = useState(false);

  return (
    <div className="mx-auto max-w-3xl p-6 lg:p-8">
      <header className="mb-4 flex items-center gap-3">
        <Shield className="h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">Roles</h1>
          <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
            Legacy role-permissions matrix. New installs should use{" "}
            <a href="/settings/groups" className="text-[var(--accent)] hover:underline">
              Groups
            </a>{" "}
            for tenancy — roles stay for back-compat with users on the role pivot.
          </p>
        </div>
      </header>

      <div className="mb-3 flex items-center justify-end">
        {canWrite && (
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="h-3 w-3" />
            New role
          </Button>
        )}
      </div>

      {list.isPending ? (
        <Skeleton className="h-32 rounded-md" />
      ) : (list.data ?? []).length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-8 text-center text-sm text-[var(--text-tertiary)]">
          No roles defined. Create one or skip — Groups cover the same surface.
        </p>
      ) : (
        <ul className="space-y-2">
          {(list.data ?? []).map((r) => (
            <li key={r.id}>
              <RoleRow role={r} canWrite={canWrite} />
            </li>
          ))}
        </ul>
      )}

      {creating && <CreateRoleDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

function RoleRow({ role, canWrite }: { role: FullRole; canWrite: boolean }) {
  const qc = useQueryClient();
  const remove = useMutation({
    mutationFn: () => api(`/api/roles/${encodeURIComponent(role.id)}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success(`Role "${role.name}" deleted`);
      qc.invalidateQueries({ queryKey: ["admin", "roles"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });
  return (
    <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <div className="flex items-center gap-3 px-4 py-3">
        <Shield className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium">{role.name}</div>
          {role.description && (
            <div className="mt-0.5 truncate font-mono text-[10px] text-[var(--text-tertiary)]">
              {role.description}
            </div>
          )}
        </div>
        <span className="shrink-0 font-mono text-[10px] text-[var(--text-tertiary)]">
          {role.permissions.length}{" "}
          {role.permissions.length === 1 ? "permission" : "permissions"}
        </span>
        {canWrite && role.name !== "admin" && (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Delete"
            onClick={() => {
              if (window.confirm(`Delete role "${role.name}"?`)) remove.mutate();
            }}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        )}
      </div>
      {role.permissions.length > 0 && (
        <div className="flex flex-wrap gap-1 border-t border-[var(--border-subtle)] px-4 py-2">
          {role.permissions.map((p, i) => (
            <span
              key={i}
              className="inline-flex h-5 items-center rounded bg-[var(--bg-tertiary)] px-1.5 font-mono text-[10px] text-[var(--text-tertiary)]"
            >
              {p.resource}:{p.action}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

function CreateRoleDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationFn: () => api("/api/roles", { method: "POST", body: { name, description } }),
    onSuccess: () => {
      toast.success(`Role "${name}" created`);
      qc.invalidateQueries({ queryKey: ["admin", "roles"] });
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Create failed"),
  });
  return (
    <div
      className="fixed inset-0 z-[55] flex items-center justify-center bg-[rgba(8,8,11,0.6)] p-4"
      onClick={onClose}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-md rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] shadow-[var(--shadow-lg)]"
      >
        <header className="border-b border-[var(--border-subtle)] px-4 py-3">
          <h2 className="text-sm font-semibold">New role</h2>
        </header>
        <div className="space-y-3 p-4">
          <div className="space-y-1">
            <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">name</div>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="ops"
              className="h-8 font-mono text-[12px]"
              autoFocus
            />
          </div>
          <div className="space-y-1">
            <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">description</div>
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="ops on-call rotation"
              className="h-8 font-mono text-[12px]"
            />
          </div>
          <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
            Permissions are added through the API today; the matrix editor lands when the
            new tenancy model fully replaces roles.
          </p>
        </div>
        <footer className="flex justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
          <Button variant="ghost" size="sm" onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button size="sm" onClick={() => create.mutate()} disabled={create.isPending || !name}>
            {create.isPending ? "Creating…" : "Create"}
          </Button>
        </footer>
      </div>
    </div>
  );
}
