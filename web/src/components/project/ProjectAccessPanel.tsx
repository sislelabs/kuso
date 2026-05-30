"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { Plus, X, Users as UsersIcon, User as UserIcon, ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";

// ProjectAccessPanel — the role-system-v2 per-project access list.
// Admins add users or groups to a project, each with an optional role
// override (defaults to "inherit the grantee's instance role"). A
// non-admin sees a project ONLY if they (or a group they're in) appear
// here. Admin-only; the parent gates rendering on settings:admin.

type ProjectRole = "admin" | "editor" | "viewer" | "";

interface Grant {
  id: string;
  project: string;
  kind: "user" | "group";
  userId?: string;
  groupId?: string;
  roleOverride: ProjectRole;
}

interface UserRow {
  id: string;
  username: string;
}
interface GroupRow {
  id: string;
  name: string;
}

const OVERRIDE_OPTIONS: { value: ProjectRole; label: string }[] = [
  { value: "", label: "inherit (instance role)" },
  { value: "viewer", label: "viewer" },
  { value: "editor", label: "editor" },
  { value: "admin", label: "admin" },
];

export function ProjectAccessPanel({ project }: { project: string }) {
  const qc = useQueryClient();
  const key = ["admin", "project-grants", project] as const;

  const grants = useQuery({
    queryKey: key,
    queryFn: () =>
      api<{ grants: Grant[] }>(`/api/projects/${encodeURIComponent(project)}/grants`).then(
        (r) => r.grants ?? []
      ),
  });
  const users = useQuery({
    queryKey: ["admin", "users"],
    queryFn: async () => {
      const res = await api<{ data?: UserRow[] } | UserRow[]>("/api/users");
      return Array.isArray(res) ? res : (res.data ?? []);
    },
  });
  const groups = useQuery({
    queryKey: ["admin", "groups"],
    queryFn: async () => {
      const res = await api<{ data?: GroupRow[] } | GroupRow[]>("/api/groups");
      return Array.isArray(res) ? res : (res.data ?? []);
    },
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: key });

  const add = useMutation({
    mutationFn: (body: { userId?: string; groupId?: string; role: ProjectRole }) =>
      api(`/api/projects/${encodeURIComponent(project)}/grants`, { method: "POST", body }),
    onSuccess: () => {
      toast.success("Access granted");
      invalidate();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "grant failed"),
  });

  const remove = useMutation({
    mutationFn: (id: string) =>
      api(`/api/projects/${encodeURIComponent(project)}/grants/${encodeURIComponent(id)}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      toast.success("Access removed");
      invalidate();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "remove failed"),
  });

  const setOverride = useMutation({
    // AddProjectGrant upserts, so re-adding the same grantee updates the
    // override.
    mutationFn: (g: Grant) =>
      api(`/api/projects/${encodeURIComponent(project)}/grants`, {
        method: "POST",
        body: g.kind === "user" ? { userId: g.userId, role: g.roleOverride } : { groupId: g.groupId, role: g.roleOverride },
      }),
    onSuccess: invalidate,
    onError: (e) => toast.error(e instanceof Error ? e.message : "update failed"),
  });

  const userName = (id?: string) => users.data?.find((u) => u.id === id)?.username ?? id ?? "?";
  const groupName = (id?: string) => groups.data?.find((g) => g.id === id)?.name ?? id ?? "?";

  if (grants.isPending) return <Skeleton className="h-32" />;

  const rows = grants.data ?? [];

  return (
    <div className="space-y-3">
      <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <header className="flex items-center gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
          <h3 className="text-sm font-semibold tracking-tight">Access</h3>
          <span className="ml-auto font-mono text-[10px] text-[var(--text-tertiary)]">
            {rows.length} {rows.length === 1 ? "grant" : "grants"}
          </span>
        </header>

        {rows.length === 0 ? (
          <p className="px-3 py-2.5 text-[11px] text-[var(--text-tertiary)]">
            No grants. This project is visible only to admins until you add a user or group
            below.
          </p>
        ) : (
          <ul>
            {rows.map((g) => (
              <GrantRow
                key={g.id}
                grant={g}
                label={g.kind === "group" ? groupName(g.groupId) : userName(g.userId)}
                onRoleChange={(role) => setOverride.mutate({ ...g, roleOverride: role })}
                onRemove={() => remove.mutate(g.id)}
              />
            ))}
          </ul>
        )}

        <AddGrantRow
          users={users.data ?? []}
          groups={groups.data ?? []}
          existing={rows}
          onAdd={(body) => add.mutate(body)}
          pending={add.isPending}
        />
      </div>
      <p className="text-[11px] leading-relaxed text-[var(--text-tertiary)]">
        Override sets the role on <span className="text-[var(--text-secondary)]">this</span>{" "}
        project. &ldquo;inherit&rdquo; uses the grantee&apos;s instance role (viewer if they
        have none). Reading env values, opening a pod shell, the SQL browser, project export,
        and triggering runs are admin-only regardless of override.
      </p>
    </div>
  );
}

interface Member {
  id: string;
  username: string;
  email?: string;
}

// GrantRow renders one access grant. For a GROUP grant the name is a
// disclosure toggle: clicking it fetches + reveals the group's members
// so an admin can see who a group-scoped grant actually covers without
// leaving the project. User grants render the same minus the toggle.
function GrantRow({
  grant,
  label,
  onRoleChange,
  onRemove,
}: {
  grant: Grant;
  label: string;
  onRoleChange: (role: ProjectRole) => void;
  onRemove: () => void;
}) {
  const [open, setOpen] = useState(false);
  const isGroup = grant.kind === "group";

  // Members load lazily on first expand (enabled-gated), cached after.
  const members = useQuery({
    queryKey: ["admin", "group-members", grant.groupId],
    queryFn: async () => {
      const res = await api<{ data?: Member[] }>(
        `/api/groups/${encodeURIComponent(grant.groupId ?? "")}/members`
      );
      return res.data ?? [];
    },
    enabled: isGroup && open && !!grant.groupId,
  });

  return (
    <li className="border-b border-[var(--border-subtle)] last:border-b-0">
      <div className="grid grid-cols-[1fr_180px_28px] items-center gap-2 px-3 py-1.5">
        {isGroup ? (
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className="flex items-center gap-1.5 truncate text-left text-[12px]"
            title={open ? "Hide members" : "Show members"}
          >
            <ChevronRight
              className={cn(
                "h-3 w-3 shrink-0 text-[var(--text-tertiary)] transition-transform",
                open && "rotate-90"
              )}
            />
            <UsersIcon className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" />
            <span className="truncate font-mono">{label}</span>
            <span className="text-[10px] text-[var(--text-tertiary)]">group</span>
          </button>
        ) : (
          <span className="flex items-center gap-1.5 truncate pl-[18px] text-[12px]">
            <UserIcon className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" />
            <span className="truncate font-mono">{label}</span>
            <span className="text-[10px] text-[var(--text-tertiary)]">user</span>
          </span>
        )}
        <select
          value={grant.roleOverride}
          onChange={(e) => onRoleChange(e.target.value as ProjectRole)}
          className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px]"
        >
          {OVERRIDE_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <button
          type="button"
          onClick={onRemove}
          aria-label="Remove access"
          className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
        >
          <X className="h-3 w-3" />
        </button>
      </div>
      {isGroup && open && (
        <div className="border-t border-[var(--border-subtle)]/50 bg-[var(--bg-primary)]/40 px-3 py-1.5 pl-[34px]">
          {members.isPending ? (
            <p className="text-[10px] text-[var(--text-tertiary)]">Loading members…</p>
          ) : (members.data ?? []).length === 0 ? (
            <p className="text-[10px] text-[var(--text-tertiary)]">
              No members. Add users to this group in Settings → Groups.
            </p>
          ) : (
            <ul className="space-y-0.5">
              {(members.data ?? []).map((m) => (
                <li
                  key={m.id}
                  className="flex items-center gap-1.5 truncate text-[11px] text-[var(--text-secondary)]"
                >
                  <UserIcon className="h-2.5 w-2.5 shrink-0 text-[var(--text-tertiary)]" />
                  <span className="truncate font-mono">{m.username}</span>
                  {m.email && (
                    <span className="truncate text-[10px] text-[var(--text-tertiary)]">
                      {m.email}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </li>
  );
}

function AddGrantRow({
  users,
  groups,
  existing,
  onAdd,
  pending,
}: {
  users: UserRow[];
  groups: GroupRow[];
  existing: Grant[];
  onAdd: (body: { userId?: string; groupId?: string; role: ProjectRole }) => void;
  pending: boolean;
}) {
  const [kind, setKind] = useState<"user" | "group">("user");
  const [granteeId, setGranteeId] = useState("");
  const [role, setRole] = useState<ProjectRole>("");

  const grantedUserIds = new Set(existing.filter((g) => g.kind === "user").map((g) => g.userId));
  const grantedGroupIds = new Set(existing.filter((g) => g.kind === "group").map((g) => g.groupId));
  const availUsers = users.filter((u) => !grantedUserIds.has(u.id));
  const availGroups = groups.filter((g) => !grantedGroupIds.has(g.id));

  const submit = () => {
    if (!granteeId) {
      toast.error("Pick a user or group");
      return;
    }
    onAdd(kind === "user" ? { userId: granteeId, role } : { groupId: granteeId, role });
    setGranteeId("");
    setRole("");
  };

  return (
    <div className="flex flex-wrap items-center gap-2 border-t border-[var(--border-subtle)] px-3 py-2">
      <select
        value={kind}
        onChange={(e) => {
          setKind(e.target.value as "user" | "group");
          setGranteeId("");
        }}
        className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 text-[11px]"
      >
        <option value="user">user</option>
        <option value="group">group</option>
      </select>
      <select
        value={granteeId}
        onChange={(e) => setGranteeId(e.target.value)}
        className="h-7 min-w-[140px] flex-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px]"
      >
        <option value="">(pick a {kind})</option>
        {kind === "user"
          ? availUsers.map((u) => (
              <option key={u.id} value={u.id}>
                {u.username}
              </option>
            ))
          : availGroups.map((g) => (
              <option key={g.id} value={g.id}>
                {g.name}
              </option>
            ))}
      </select>
      <select
        value={role}
        onChange={(e) => setRole(e.target.value as ProjectRole)}
        className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px]"
      >
        {OVERRIDE_OPTIONS.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      <Button size="sm" variant="secondary" disabled={pending} onClick={submit}>
        <Plus className="h-3 w-3" /> add
      </Button>
    </div>
  );
}
