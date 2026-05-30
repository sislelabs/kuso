"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { Users, Plus, X, Save, ShieldCheck, Check } from "lucide-react";
import { cn } from "@/lib/utils";

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
    onError: (e) => toast.error(e instanceof Error ? e.message : "create failed"),
  });

  return (
    <div className="mx-auto max-w-5xl p-6 lg:p-8">
      <header className="mb-6 flex items-center gap-3">
        <Users className="h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">Groups</h1>
          <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
            Instance-wide groups. Each group carries a kuso-wide instance role plus
            per-project memberships; a user&apos;s effective perms are the union across
            every group they&apos;re in. Use this to grant access to specific projects or
            promote someone to admin.
          </p>
        </div>
      </header>

      <div className="grid grid-cols-[280px_1fr] gap-4">
        {/* Left: group list */}
        <aside className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
          <div className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
            <h2 className="text-sm font-semibold tracking-tight">Groups</h2>
            <button
              type="button"
              onClick={() => setCreating(true)}
              className="inline-flex items-center gap-1 text-[11px] text-[var(--accent)] hover:underline"
            >
              <Plus className="h-3 w-3" /> new
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
                className="h-7 flex-1 text-[12px]"
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
          ) : (groups.data ?? []).length === 0 ? (
            <p className="px-3 py-4 text-[11px] text-[var(--text-tertiary)]">No groups yet.</p>
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
                        "block w-full truncate border-b border-[var(--border-subtle)] px-3 py-2 text-left text-[12px] last:border-b-0",
                        active
                          ? "bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                          : "hover:bg-[var(--bg-tertiary)]"
                      )}
                    >
                      <div className="font-medium">{g.name}</div>
                      {g.description && (
                        <div className="mt-0.5 truncate text-[10px] text-[var(--text-tertiary)]">
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
            <GroupEditor groupId={selected} />
          ) : (
            <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-8 text-center text-sm text-[var(--text-tertiary)]">
              Pick a group on the left, or click <span className="font-mono">+ new</span> to create one.
            </p>
          )}
        </main>
      </div>
    </div>
  );
}

function GroupEditor({ groupId }: { groupId: string }) {
  const qc = useQueryClient();
  const tenancy = useQuery({
    queryKey: ["admin", "groups", groupId, "tenancy"],
    queryFn: () => api<GroupTenancy>(`/api/groups/${encodeURIComponent(groupId)}/tenancy`),
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
    onError: (e) => toast.error(e instanceof Error ? e.message : "save failed"),
  });

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
                  "flex w-full items-center justify-between gap-3 rounded-md px-3 py-1.5 text-left text-[12px] transition-colors",
                  active
                    ? "bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                    : "hover:bg-[var(--bg-tertiary)]/50"
                )}
              >
                <span className="font-mono">{r.label}</span>
                <span className="ml-auto truncate text-[10px] text-[var(--text-tertiary)]">
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
        <p className="px-3 py-2.5 text-[11px] leading-relaxed text-[var(--text-tertiary)]">
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
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
          {dirty ? "unsaved changes" : "saved"}
        </span>
        <Button
          size="sm"
          disabled={!dirty || save.isPending}
          onClick={() => save.mutate(form)}
        >
          <Save className="h-3 w-3" />
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      </div>
    </div>
  );
}

function MembersSection({ groupId, users }: { groupId: string; users: UserRow[] }) {
  // We don't yet have a "members of this group" list endpoint —
  // the user → group join is one-way through /api/users/profile.
  // Workaround: surface the `add a user` action and trust the
  // invalidation to refresh on next page load. A future endpoint
  // /api/groups/{id}/members would close this loop.
  const qc = useQueryClient();
  const [picked, setPicked] = useState<string>("");
  const add = useMutation({
    mutationFn: (userId: string) =>
      api(
        `/api/groups/${encodeURIComponent(groupId)}/members/${encodeURIComponent(userId)}`,
        { method: "POST" }
      ),
    onSuccess: () => {
      toast.success("Member added");
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      setPicked("");
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "add failed"),
  });
  const remove = useMutation({
    mutationFn: (userId: string) =>
      api(
        `/api/groups/${encodeURIComponent(groupId)}/members/${encodeURIComponent(userId)}`,
        { method: "DELETE" }
      ),
    onSuccess: () => {
      toast.success("Member removed");
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "remove failed"),
  });
  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="flex items-center gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
        <h3 className="text-sm font-semibold tracking-tight">Members</h3>
      </header>
      <div className="space-y-2 p-3">
        <div className="flex items-center gap-2">
          <select
            value={picked}
            onChange={(e) => setPicked(e.target.value)}
            className="h-7 flex-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px]"
          >
            <option value="">(pick a user to add)</option>
            {users.map((u) => (
              <option key={u.id} value={u.id}>
                {u.username} {u.email ? `· ${u.email}` : ""}
              </option>
            ))}
          </select>
          <Button
            size="sm"
            type="button"
            disabled={!picked || add.isPending}
            onClick={() => picked && add.mutate(picked)}
          >
            <Plus className="h-3 w-3" /> Add
          </Button>
        </div>
        <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
          Removing a member uses the same dropdown — pick a user and click Remove.
        </p>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            type="button"
            disabled={!picked || remove.isPending}
            onClick={() => picked && remove.mutate(picked)}
          >
            <X className="h-3 w-3" /> Remove
          </Button>
        </div>
      </div>
    </section>
  );
}
