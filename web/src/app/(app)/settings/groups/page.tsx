"use client";

import { SettingsPage } from "@/components/settings/SettingsPage";
import { UserAvatar } from "@/components/activity/UserBadge";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { LoadingState } from "@/components/ui/loading-state";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { Plus, X, Save, ShieldCheck, Check, UserPlus, Trash2 } from "lucide-react";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { cn } from "@/lib/utils";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

interface Group {
  id: string;
  name: string;
  description?: string;
}

type InstanceRoleValue = "admin" | "editor" | "viewer" | "";

interface GroupTenancy {
  instanceRole: InstanceRoleValue;
  // Legacy field — still returned by GET /tenancy but no longer the
  // source of project access in role-system v2. Project access is
  // managed per-project via the project Access panel (ProjectGrant).
  projectMemberships?: { project: string; role: string }[];
}

interface UserRow {
  id: string;
  username: string;
  email?: string;
}

const INSTANCE_ROLES: { value: InstanceRoleValue; label: string; hint: string }[] = [
  { value: "admin",  label: "admin",  hint: "full access to everything, all projects, all settings" },
  { value: "editor", label: "editor", hint: "default level on granted projects: read+write (no env values / shell / SQL)" },
  { value: "viewer", label: "viewer", hint: "default level on granted projects: read-only" },
  { value: "",       label: "none",   hint: "no access until added to a project" },
];

// /settings/groups — admins manage who can do what.
export default function GroupsSettingsPage() {
  const groups = useQuery({
    queryKey: ["admin", "groups"],
    queryFn: async () => {
      const res = await api<{ data?: Group[] } | Group[]>("/api/groups");
      // Some endpoints wrap in {success, data}; handle both.
      return Array.isArray(res) ? res : (res.data ?? []);
    },
  });
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");

  const qc = useQueryClient();
  const create = useMutation({
    mutationFn: (name: string) =>
      api<{ id: string }>("/api/groups", { method: "POST", body: { name } }),
    onSuccess: (g) => {
      toast.success(`Group ${g.id} created`);
      qc.invalidateQueries({ queryKey: ["admin", "groups"] });
      setSelected(g.id);
      setCreating(false);
      setNewName("");
    },
    onError: (e) => toastError(e, "Create failed"),
  });

  return (
    <SettingsPage
      title="Groups"
      description="Each group carries an instance role; project access is granted on each project. A user's effective permissions are the union across every group they're in."
    >
      <div className="grid grid-cols-1 gap-4 md:grid-cols-[240px_1fr]">
        {/* Left: group list */}
        <aside className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
          <div className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
            <h2 className="text-sm font-semibold tracking-tight">Groups</h2>
            <button
              type="button"
              onClick={() => setCreating(true)}
              className="inline-flex items-center gap-1 text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
            >
              <Plus className="h-3 w-3" /> New
            </button>
          </div>
          {creating && (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                if (newName.trim()) create.mutate(newName.trim());
              }}
              className="flex items-center gap-1 border-b border-[var(--border-subtle)] px-2 py-1.5"
            >
              <Input
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                placeholder="group name"
                className="h-7 flex-1 text-xs"
                autoFocus
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    setCreating(false);
                    setNewName("");
                  }
                }}
              />
              <button
                type="submit"
                disabled={create.isPending || !newName.trim()}
                aria-label="Create group"
                title="Create (Enter)"
                className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--accent)] hover:bg-[var(--accent-subtle)] disabled:opacity-30 disabled:hover:bg-transparent"
              >
                {create.isPending ? (
                  <span className="h-3 w-3 animate-spin rounded-full border border-[var(--accent)] border-t-transparent" />
                ) : (
                  <Check className="h-3.5 w-3.5" />
                )}
              </button>
              <button
                type="button"
                onClick={() => {
                  setCreating(false);
                  setNewName("");
                }}
                aria-label="Cancel"
                title="Cancel (Esc)"
                className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </form>
          )}
          {groups.isPending ? (
            <Skeleton className="m-3 h-24" />
          ) : groups.isError ? (
            <QueryErrorState what="groups" error={groups.error} onRetry={() => void groups.refetch()} className="m-3" />
          ) : (groups.data ?? []).length === 0 ? (
            <p className="px-3 py-4 text-sm text-[var(--text-tertiary)]">No groups yet.</p>
          ) : (
            <ul>
              {(groups.data ?? []).map((g) => {
                const active = g.id === selected;
                return (
                  <li key={g.id}>
                    <button
                      type="button"
                      onClick={() => setSelected(g.id)}
                      className={cn(
                        "block w-full truncate border-b border-[var(--border-subtle)] px-3 py-2 text-left text-sm last:border-b-0",
                        active
                          ? "bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                          : "hover:bg-[var(--bg-tertiary)]"
                      )}
                    >
                      <div className="font-medium">{g.name}</div>
                      {g.description && (
                        <div className="mt-0.5 truncate text-xs text-[var(--text-tertiary)]">
                          {g.description}
                        </div>
                      )}
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </aside>

        {/* Right: editor */}
        <main>
          {selected ? (
            <GroupEditor
              key={selected}
              groupId={selected}
              groupName={(groups.data ?? []).find((g) => g.id === selected)?.name ?? selected}
              onDeleted={() => setSelected(null)}
            />
          ) : (
            <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-8 text-center text-sm text-[var(--text-tertiary)]">
              Pick a group on the left, or click <span className="font-medium text-[var(--text-secondary)]">New</span> to create one.
            </p>
          )}
        </main>
      </div>
    </SettingsPage>
  );
}

function GroupEditor({
  groupId,
  groupName,
  onDeleted,
}: {
  groupId: string;
  groupName: string;
  onDeleted: () => void;
}) {
  const qc = useQueryClient();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const del = useMutation({
    mutationFn: () => api(`/api/groups/${encodeURIComponent(groupId)}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success(`Group ${groupName} deleted`);
      setConfirmDelete(false);
      qc.invalidateQueries({ queryKey: ["admin", "groups"] });
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      onDeleted();
    },
    onError: (e) => toastError(e, "Delete failed"),
  });
  const tenancy = useQuery({
    queryKey: ["admin", "groups", groupId, "tenancy"],
    queryFn: async () => {
      const t = await api<GroupTenancy>(`/api/groups/${encodeURIComponent(groupId)}/tenancy`);
      // The DB default ("member") and the pending group's "pending" grant
      // no instance-level access, so they render as "none".
      const known = INSTANCE_ROLES.some((r) => r.value === t.instanceRole);
      return known ? t : { ...t, instanceRole: "" as const };
    },
  });
  const users = useQuery({
    queryKey: ["admin", "users"],
    queryFn: async () => {
      const res = await api<{ data?: UserRow[] } | UserRow[]>("/api/users");
      return Array.isArray(res) ? res : (res.data ?? []);
    },
  });

  // Local form state, separate from server state so the user can
  // diff before saving. baseline updates on a successful save +
  // when the user picks a different group.
  const [form, setForm] = useState<GroupTenancy | null>(null);
  const [baseline, setBaseline] = useState<GroupTenancy | null>(null);
  if (tenancy.data && !form) {
    setForm(tenancy.data);
    setBaseline(tenancy.data);
  }

  const save = useMutation({
    // v2: set just the instance role via the dedicated endpoint. Project
    // access is no longer edited here — it lives on each project's Access
    // panel (ProjectGrant). The legacy projectMemberships JSON is left
    // untouched (the resolver ignores it).
    mutationFn: (body: GroupTenancy) =>
      api(`/api/groups/${encodeURIComponent(groupId)}/instance-role`, {
        method: "PUT",
        body: { role: body.instanceRole },
      }),
    onSuccess: () => {
      toast.success("Saved");
      setBaseline(form);
      qc.invalidateQueries({ queryKey: ["admin", "groups", groupId, "tenancy"] });
    },
    onError: (e) => toastError(e, "Save failed"),
  });

  if (tenancy.isError) {
    return (
      <QueryErrorState what="group" error={tenancy.error} onRetry={() => void tenancy.refetch()} />
    );
  }
  if (tenancy.isPending || !form) {
    return <Skeleton className="h-72" />;
  }

  const dirty = JSON.stringify(form) !== JSON.stringify(baseline);

  return (
    <div className="space-y-4">
      {/* Instance role */}
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <header className="flex items-center gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
          <ShieldCheck className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
          <h3 className="text-sm font-semibold tracking-tight">Instance role</h3>
        </header>
        <div className="space-y-1 p-2">
          {INSTANCE_ROLES.map((r) => {
            const active = form.instanceRole === r.value;
            return (
              <button
                key={r.value}
                type="button"
                onClick={() => setForm({ ...form, instanceRole: r.value })}
                className={cn(
                  "flex w-full flex-wrap items-center justify-between gap-x-3 gap-y-0.5 rounded-md px-3 py-1.5 text-left text-sm transition-colors",
                  active
                    ? "bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                    : "hover:bg-[var(--bg-tertiary)]/50"
                )}
              >
                <span className="font-medium">{r.label}</span>
                <span className="ml-auto text-xs text-[var(--text-tertiary)]">
                  {r.hint}
                </span>
              </button>
            );
          })}
        </div>
      </section>

      {/* Project access (v2): managed per-project, not here. */}
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <header className="flex items-center gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
          <h3 className="text-sm font-semibold tracking-tight">Project access</h3>
        </header>
        <p className="px-3 py-2.5 text-sm leading-relaxed text-[var(--text-secondary)]">
          Projects are invisible to non-admins until this group is granted access on the
          project itself. Open a project, then use its{" "}
          <span className="text-[var(--text-secondary)]">Access</span> panel to add this group
          (with an optional per-project role that overrides the instance role above). The
          instance role here is the default level applied on every project the group is
          granted.
        </p>
      </section>

      {/* Members — listed users + add/remove (rough first cut: chip
          field with usernames). */}
      <MembersSection groupId={groupId} users={users.data ?? []} />

      {/* Save bar — sticky at bottom of editor, matches the service settings panel. */}
      <div className="flex items-center justify-end gap-2">
        <Button
          size="sm"
          variant="ghost"
          className="mr-auto text-[var(--error)]"
          onClick={() => setConfirmDelete(true)}
        >
          <Trash2 className="h-3 w-3" />
          Delete group
        </Button>
        <span className="text-xs text-[var(--text-secondary)]">
          {dirty ? "Unsaved changes" : "Saved"}
        </span>
        <Button
          size="sm"
          disabled={!dirty || save.isPending}
          disabledReason={!dirty && !save.isPending ? "No changes to save" : undefined}
          onClick={() => save.mutate(form)}
        >
          <Save className="h-3 w-3" />
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      </div>
      <ConfirmDialog
        open={confirmDelete}
        title={`Delete group ${groupName}?`}
        body={
          <p>
            Removes the group and all its memberships. Members lose any access that came
            only from this group, and their current sessions are signed out. User accounts
            are not deleted.
          </p>
        }
        typeToConfirm={groupName}
        confirmLabel="Delete group"
        destructive
        pending={del.isPending}
        onConfirm={() => del.mutate()}
        onCancel={() => setConfirmDelete(false)}
      />
    </div>
  );
}

function MembersSection({ groupId, users }: { groupId: string; users: UserRow[] }) {
  const qc = useQueryClient();
  const membersKey = ["admin", "group-members", groupId] as const;

  // The roster, straight from the group → members endpoint (added so
  // the UI can show who's in a group, not just blindly add/remove).
  const members = useQuery({
    queryKey: membersKey,
    queryFn: async () => {
      const res = await api<{ data?: UserRow[] }>(
        `/api/groups/${encodeURIComponent(groupId)}/members`
      );
      return res.data ?? [];
    },
  });

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: membersKey });
    qc.invalidateQueries({ queryKey: ["admin", "users"] });
  };
  const add = useMutation({
    mutationFn: (userId: string) =>
      api(
        `/api/groups/${encodeURIComponent(groupId)}/members/${encodeURIComponent(userId)}`,
        { method: "POST" }
      ),
    onSuccess: () => {
      toast.success("Member added");
      invalidate();
    },
    onError: (e) => toastError(e, "Add failed"),
  });
  const [removing, setRemoving] = useState<{ id: string; username: string } | null>(null);
  const remove = useMutation({
    mutationFn: (userId: string) =>
      api(
        `/api/groups/${encodeURIComponent(groupId)}/members/${encodeURIComponent(userId)}`,
        { method: "DELETE" }
      ),
    onSuccess: () => {
      toast.success("Member removed");
      setRemoving(null);
      invalidate();
    },
    onError: (e) => toastError(e, "Remove failed"),
  });

  const roster = members.data ?? [];
  const memberIds = new Set(roster.map((m) => m.id));
  const addable = users.filter((u) => !memberIds.has(u.id));

  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="flex items-center gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
        <h3 className="text-sm font-semibold tracking-tight">Members</h3>
        <span className="ml-auto text-xs text-[var(--text-secondary)]">
          {roster.length} {roster.length === 1 ? "member" : "members"}
        </span>
      </header>

      {/* Roster — one row per member with an inline remove. */}
      {members.isPending ? (
        <LoadingState kind="inline" className="px-3 py-2.5" label="loading members…" />
      ) : members.isError ? (
        <QueryErrorState
          what="members"
          error={members.error}
          onRetry={() => void members.refetch()}
          className="m-3"
        />
      ) : roster.length === 0 ? (
        <p className="px-3 py-2.5 text-xs text-[var(--text-tertiary)]">
          No members yet. Add a user below.
        </p>
      ) : (
        <ul>
          {roster.map((m) => (
            <li
              key={m.id}
              className="flex items-center gap-2 border-b border-[var(--border-subtle)] px-3 py-1.5 last:border-b-0"
            >
              <UserAvatar user={{ username: m.username }} />
              <span className="truncate text-sm">{m.username}</span>
              {m.email && (
                <span className="truncate text-xs text-[var(--text-tertiary)]">{m.email}</span>
              )}
              <button
                type="button"
                onClick={() => setRemoving({ id: m.id, username: m.username })}
                disabled={remove.isPending}
                aria-label={`Remove ${m.username}`}
                title={`Remove ${m.username} from group`}
                className="ml-auto inline-flex h-6 w-6 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--error)]"
              >
                <X className="h-3 w-3" />
              </button>
            </li>
          ))}
        </ul>
      )}

      {/* Add row — a popover of non-members; clicking one adds it
          immediately and keeps the panel open so several can be added
          in a row (no Add button, no close-on-select). */}
      <div className="border-t border-[var(--border-subtle)] px-3 py-2">
        <Popover>
          <PopoverTrigger
            disabled={addable.length === 0}
            className={cn(
              "inline-flex h-7 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 text-xs text-[var(--text-secondary)]",
              "hover:border-[var(--border-strong)] hover:text-[var(--text-primary)] data-[popup-open]:border-[var(--border-strong)]",
              addable.length === 0 && "pointer-events-none opacity-40"
            )}
          >
            <UserPlus className="h-3 w-3" />
            {addable.length === 0 ? "all users are members" : "add member"}
          </PopoverTrigger>
          <PopoverContent align="start" sideOffset={4} className="max-h-72 w-64 gap-0.5 overflow-y-auto rounded-md p-1">
            {addable.map((u) => (
              <button
                key={u.id}
                type="button"
                onClick={() => add.mutate(u.id)}
                disabled={add.isPending}
                className="flex w-full items-center gap-1.5 rounded px-1.5 py-1 text-left text-xs text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <Plus className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" />
                <span className="truncate font-mono">{u.username}</span>
                {u.email && (
                  <span className="truncate text-xs text-[var(--text-tertiary)]">{u.email}</span>
                )}
              </button>
            ))}
          </PopoverContent>
        </Popover>
      </div>

      <ConfirmDialog
        open={removing !== null}
        title={`Remove ${removing?.username ?? "member"} from this group?`}
        body={<p>They lose the group&apos;s instance role and project grants on their next request.</p>}
        confirmLabel="Remove member"
        destructive
        pending={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
        onCancel={() => setRemoving(null)}
      />
    </section>
  );
}
