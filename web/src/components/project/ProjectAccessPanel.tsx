"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { X, Users as UsersIcon, User as UserIcon, ChevronRight, UserPlus } from "lucide-react";
import { cn } from "@/lib/utils";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

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

  // Distinguish a failed fetch from a genuinely empty grant list. Without
  // this an error falls through to "No grants" — which reads as "this
  // project is admin-only", the OPPOSITE of "we couldn't load access
  // control". An admin must not be told access is locked down when we
  // simply failed to read it.
  if (grants.isError) {
    return (
      <div className="rounded-md border border-red-500/30 bg-red-500/5 p-4 text-[12px]">
        <p className="text-red-400">
          Couldn&apos;t load project access:{" "}
          {grants.error instanceof Error ? grants.error.message : "request failed"}
        </p>
        <button
          type="button"
          onClick={() => grants.refetch()}
          className="mt-2 font-mono text-[11px] text-[var(--accent)] hover:underline"
        >
          Retry
        </button>
      </div>
    );
  }

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
  // One popover lists every grantable user AND group (already-granted
  // ones filtered out). Clicking a row grants it immediately at the
  // default "inherit" role and keeps the panel open, so an admin can
  // add several grantees in one go; the per-row role override is then
  // tuned in the list above. No kind toggle, no separate role select,
  // no add button — the icon distinguishes user from group.
  const grantedUserIds = new Set(existing.filter((g) => g.kind === "user").map((g) => g.userId));
  const grantedGroupIds = new Set(existing.filter((g) => g.kind === "group").map((g) => g.groupId));
  const availUsers = users.filter((u) => !grantedUserIds.has(u.id));
  const availGroups = groups.filter((g) => !grantedGroupIds.has(g.id));
  const nothingToAdd = availUsers.length === 0 && availGroups.length === 0;

  return (
    <div className="border-t border-[var(--border-subtle)] px-3 py-2">
      <Popover>
        <PopoverTrigger
          disabled={pending || nothingToAdd}
          className={cn(
            "inline-flex h-7 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 text-[11px] text-[var(--text-secondary)]",
            "hover:border-[var(--border-strong)] hover:text-[var(--text-primary)] data-[popup-open]:border-[var(--border-strong)]",
            (pending || nothingToAdd) && "pointer-events-none opacity-40"
          )}
        >
          <UserPlus className="h-3 w-3" />
          {nothingToAdd ? "everyone has access" : "add access"}
        </PopoverTrigger>
        <PopoverContent
          align="start"
          sideOffset={4}
          className="max-h-72 w-64 gap-0.5 overflow-y-auto rounded-md p-1"
        >
          {availGroups.length > 0 && (
            <div className="px-1.5 pb-0.5 pt-1 font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
              groups
            </div>
          )}
          {availGroups.map((g) => (
            <button
              key={g.id}
              type="button"
              onClick={() => onAdd({ groupId: g.id, role: "" })}
              disabled={pending}
              className="flex w-full items-center gap-1.5 rounded px-1.5 py-1 text-left text-[11px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
            >
              <UsersIcon className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" />
              <span className="truncate font-mono">{g.name}</span>
            </button>
          ))}
          {availUsers.length > 0 && (
            <div className="px-1.5 pb-0.5 pt-1 font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
              users
            </div>
          )}
          {availUsers.map((u) => (
            <button
              key={u.id}
              type="button"
              onClick={() => onAdd({ userId: u.id, role: "" })}
              disabled={pending}
              className="flex w-full items-center gap-1.5 rounded px-1.5 py-1 text-left text-[11px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
            >
              <UserIcon className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" />
              <span className="truncate font-mono">{u.username}</span>
            </button>
          ))}
        </PopoverContent>
      </Popover>
    </div>
  );
}
