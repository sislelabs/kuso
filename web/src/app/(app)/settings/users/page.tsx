"use client";

import { Switch } from "@/components/ui/switch";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import { resetUserPassword } from "@/features/profile/api";
import { toast } from "sonner";
import { Plus, Trash2, KeyRound, Copy, Check } from "lucide-react";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { relativeTime } from "@/lib/format";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { EmptyState } from "@/components/shared/EmptyState";
import { toGoDuration } from "./duration";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsNotice, SettingsSection, SettingsSelect } from "@/components/settings/SettingsSection";
import { UserAvatar, UserBadge } from "@/components/activity/UserBadge";

interface UserRow {
  id: string;
  username: string;
  email?: string;
  isActive: boolean;
  // The list endpoint sends the legacy role as `role`. provider/lastLogin/
  // groups aren't sent by summariseUsers yet; the badges render once they are.
  provider?: string;
  role?: string;
  // v2 direct instance role: "admin" | "editor" | "viewer" | "" (inherit
  // from groups). Admin-editable; "admin" lets the user see every project.
  instanceRole?: string;
  createdAt?: string;
  lastLogin?: string;
  groups?: string[];
}

interface ListResp {
  data?: UserRow[];
}

export default function UsersPage() {
  const canWrite = useCan(Perms.UserWrite);
  const list = useQuery({
    queryKey: ["admin", "users"],
    queryFn: async () => {
      const res = await api<ListResp | UserRow[]>("/api/users");
      return Array.isArray(res) ? res : (res.data ?? []);
    },
  });
  const [showCreate, setShowCreate] = useState(false);
  const [resetting, setResetting] = useState<string | null>(null);

  const users = list.data ?? [];

  return (
    <SettingsPage
      title="Users"
      description="Local accounts and OAuth-created users. Instance roles and group memberships set what they can do."
      actions={
        canWrite ? (
          <Button size="sm" onClick={() => setShowCreate(true)}>
            <Plus className="h-3 w-3" />
            New user
          </Button>
        ) : undefined
      }
    >
      {!canWrite && (
        <SettingsNotice tone="warning" title="Read-only">
          Managing users needs the <code className="font-mono text-xs">user:write</code> permission.
        </SettingsNotice>
      )}

      {list.isPending ? (
        <Skeleton className="h-48 rounded-md" />
      ) : list.isError ? (
        <QueryErrorState what="users" error={list.error} onRetry={() => void list.refetch()} />
      ) : users.length === 0 ? (
        <EmptyState title="No users" description="Create a local user or send an invite link." className="p-8" />
      ) : (
        <div
          role="table"
          aria-label="Users"
          className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
        >
          <div
            role="row"
            className={cn(
              "hidden border-b border-[var(--border-subtle)] px-4 py-2 text-xs font-medium text-[var(--text-secondary)] md:grid",
              USER_GRID,
            )}
          >
            <span role="columnheader">User</span>
            <span role="columnheader">Instance role</span>
            <span role="columnheader">Groups</span>
            <span role="columnheader">Last login</span>
            <span role="columnheader" className="text-right">
              <span className="sr-only">Actions</span>
            </span>
          </div>
          <div role="rowgroup" className="divide-y divide-[var(--border-subtle)]">
            {users.map((u) => (
              <UserRowItem key={u.id} u={u} canWrite={canWrite} onReset={() => setResetting(u.id)} />
            ))}
          </div>
        </div>
      )}

      {canWrite && <InvitesSection />}

      {showCreate && <CreateUserDialog onClose={() => setShowCreate(false)} />}
      {resetting && (
        <ResetPasswordDialog
          userId={resetting}
          username={users.find((u) => u.id === resetting)?.username ?? ""}
          onClose={() => setResetting(null)}
        />
      )}
    </SettingsPage>
  );
}

const USER_GRID = "md:grid-cols-[minmax(0,1.6fr)_176px_minmax(0,1fr)_96px_auto] md:items-center md:gap-4";

// ---------- Invitations ----------

interface InviteRow {
  id: string;
  token: string;
  url: string;
  groupId?: { String?: string; Valid?: boolean } | null;
  instanceRole?: { String?: string; Valid?: boolean } | null;
  createdBy: string;
  createdAt: string;
  expiresAt?: { Time?: string; Valid?: boolean } | null;
  maxUses: number;
  usedCount: number;
  revokedAt?: { Time?: string; Valid?: boolean } | null;
  note?: { String?: string; Valid?: boolean } | null;
}

interface GroupRow {
  id: string;
  name: string;
}

function InvitesSection() {
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["admin", "invites"],
    queryFn: () => api<InviteRow[]>("/api/invites"),
  });
  const [showNew, setShowNew] = useState(false);
  const [pendingRevoke, setPendingRevoke] = useState<InviteRow | null>(null);

  const revoke = useMutation({
    mutationFn: (id: string) => api(`/api/invites/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Invite revoked");
      setPendingRevoke(null);
      qc.invalidateQueries({ queryKey: ["admin", "invites"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Revoke failed"),
  });

  const items = list.data ?? [];

  return (
    <SettingsSection
      title="Invitations"
      description="Links that let people sign up directly into a group."
      flush
      actions={
        <Button size="sm" variant="outline" onClick={() => setShowNew(true)}>
          <Plus className="h-3 w-3" />
          New invite
        </Button>
      }
    >
      {list.isPending ? (
        <Skeleton className="m-4 h-24 rounded-md" />
      ) : list.isError ? (
        <QueryErrorState what="invites" error={list.error} onRetry={() => void list.refetch()} className="m-4" />
      ) : items.length === 0 ? (
        <EmptyState
          title="No invites yet"
          description="Mint an invite link to bring a teammate onto this instance."
          className="border-0 p-6"
        />
      ) : (
        <ul className="divide-y divide-[var(--border-subtle)]">
          {items.map((inv) => (
            <li key={inv.id}>
              <InviteRowItem inv={inv} onRevoke={() => setPendingRevoke(inv)} />
            </li>
          ))}
        </ul>
      )}

      {showNew && <CreateInviteDialog onClose={() => setShowNew(false)} />}
      <ConfirmDialog
        open={pendingRevoke !== null}
        title="Revoke this invite?"
        body={<p>The link stops working immediately. This cannot be undone; mint a new invite if you need one.</p>}
        confirmLabel="Revoke invite"
        destructive
        pending={revoke.isPending}
        onConfirm={() => {
          if (pendingRevoke) revoke.mutate(pendingRevoke.id);
        }}
        onCancel={() => setPendingRevoke(null)}
      />
    </SettingsSection>
  );
}

function nullStr(v: { String?: string; Valid?: boolean } | null | undefined): string {
  if (!v || !v.Valid) return "";
  return v.String ?? "";
}

function nullTime(v: { Time?: string; Valid?: boolean } | null | undefined): string {
  if (!v || !v.Valid) return "";
  return v.Time ?? "";
}

function InviteRowItem({ inv, onRevoke }: { inv: InviteRow; onRevoke: () => void }) {
  const [copied, setCopied] = useState(false);
  const revoked = !!nullTime(inv.revokedAt);
  const expired = (() => {
    const exp = nullTime(inv.expiresAt);
    return exp ? new Date(exp) < new Date() : false;
  })();
  const exhausted = inv.usedCount >= inv.maxUses;
  const dead = revoked || expired || exhausted;
  const status = revoked ? "revoked" : expired ? "expired" : exhausted ? "used up" : "active";
  const role = nullStr(inv.instanceRole);
  const note = nullStr(inv.note);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(inv.url);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Clipboard unavailable — select the link and copy it by hand");
    }
  };

  const expiresAt = nullTime(inv.expiresAt);
  const statusTone =
    status === "active"
      ? "bg-[var(--success-subtle)] text-[var(--success)]"
      : "bg-[var(--bg-tertiary)] text-[var(--text-secondary)]";

  return (
    <div className={cn("flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-start sm:px-5", dead && "opacity-70")}>
      <div className="min-w-0 flex-1 space-y-1.5">
        <div className="flex min-w-0 items-center gap-1.5">
          <code className="min-w-0 truncate rounded-sm bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-xs text-[var(--text-primary)]">
            {inv.url}
          </code>
          <button
            type="button"
            onClick={copy}
            aria-label="Copy invite link"
            title="Copy invite link"
            className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-sm text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            {copied ? <Check className="h-3.5 w-3.5 text-[var(--success)]" /> : <Copy className="h-3.5 w-3.5" />}
          </button>
        </div>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--text-secondary)]">
          <span className={cn("rounded-sm px-1.5 py-0.5 font-medium capitalize", statusTone)}>{status}</span>
          <span>
            {inv.usedCount} of {inv.maxUses} {inv.maxUses === 1 ? "use" : "uses"}
          </span>
          {role && <span>Role {role}</span>}
          {expiresAt && (
            <span title={new Date(expiresAt).toLocaleString()}>
              {expired ? "Expired" : "Expires"} {relativeTime(expiresAt)}
            </span>
          )}
          <span className="inline-flex items-center gap-1" title={inv.createdAt}>
            Created {relativeTime(inv.createdAt)} by <UserBadge id={inv.createdBy} />
          </span>
        </div>
        {note && <p className="text-xs italic text-[var(--text-secondary)]">&ldquo;{note}&rdquo;</p>}
      </div>
      {!dead && (
        <Button variant="outline" size="sm" onClick={onRevoke} className="self-start">
          <Trash2 className="h-3 w-3" /> Revoke
        </Button>
      )}
    </div>
  );
}

function CreateInviteDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const groups = useQuery({
    queryKey: ["admin", "groups"],
    queryFn: () => api<GroupRow[]>("/api/groups"),
  });
  const [groupId, setGroupId] = useState("");
  const [instanceRole, setInstanceRole] = useState("");
  const [expiresIn, setExpiresIn] = useState("168h"); // 7 days default
  const [maxUses, setMaxUses] = useState(1);
  const [note, setNote] = useState("");

  const create = useMutation({
    mutationFn: () =>
      api<{ url: string }>("/api/invites", {
        method: "POST",
        body: {
          groupId: groupId || undefined,
          instanceRole: instanceRole || undefined,
          expiresIn: toGoDuration(expiresIn) || undefined,
          maxUses,
          note: note || undefined,
        },
      }),
    onSuccess: async (res) => {
      try {
        await navigator.clipboard.writeText(res.url);
        toast.success("Invite URL copied to clipboard");
      } catch {
        toast.success(`Invite created: ${res.url}`);
      }
      qc.invalidateQueries({ queryKey: ["admin", "invites"] });
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Create failed"),
  });

  return (
    <Modal title="New invitation" onClose={onClose} busy={create.isPending}>
      <div className="space-y-4">
        <Field label="Group" hint="The invitee joins this group on sign-up. Leave pending to approve them later.">
          <SettingsSelect value={groupId} onChange={(e) => setGroupId(e.target.value)}>
            <option value="">Pending (an admin approves later)</option>
            {(groups.data ?? []).map((g) => (
              <option key={g.id} value={g.id}>
                {g.name}
              </option>
            ))}
          </SettingsSelect>
        </Field>
        <Field label="Instance role" hint="Overrides the group's default role.">
          <SettingsSelect value={instanceRole} onChange={(e) => setInstanceRole(e.target.value)}>
            <option value="">Use the group default</option>
            <option value="admin">Admin</option>
            <option value="editor">Editor</option>
            <option value="viewer">Viewer</option>
          </SettingsSelect>
        </Field>
        <Field label="Expires in" hint="A duration like 24h, 7d or 30d. Leave empty for no expiry.">
          <Input
            value={expiresIn}
            onChange={(e) => setExpiresIn(e.target.value)}
            className="font-mono"
            placeholder="e.g. 168h"
          />
        </Field>
        <Field label="Max uses" hint="Multi-use links need an expiry.">
          <Input
            type="number"
            min={1}
            max={1000}
            value={maxUses}
            onChange={(e) => setMaxUses(Number(e.target.value) || 1)}
            className="font-mono"
          />
        </Field>
        <Field label="Note" hint="Optional. Shown to the invitee on the sign-up page.">
          <Input
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder="e.g. team onboarding"
          />
        </Field>
      </div>
      <footer className="mt-4 flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onClose} disabled={create.isPending}>
          Cancel
        </Button>
        <Button
          size="sm"
          onClick={() => create.mutate()}
          disabled={create.isPending || (maxUses > 1 && !expiresIn)}
        >
          {create.isPending ? "Creating…" : "Create + copy URL"}
        </Button>
      </footer>
    </Modal>
  );
}

function UserRowItem({
  u,
  canWrite,
  onReset,
}: {
  u: UserRow;
  canWrite: boolean;
  onReset: () => void;
}) {
  const qc = useQueryClient();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [confirmDisable, setConfirmDisable] = useState(false);
  const remove = useMutation({
    mutationFn: () => api(`/api/users/id/${encodeURIComponent(u.id)}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success(`${u.username} deleted`);
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      setConfirmOpen(false);
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });
  const toggleActive = useMutation({
    mutationFn: (isActive: boolean) =>
      api(`/api/users/id/${encodeURIComponent(u.id)}`, {
        method: "PUT",
        body: { isActive },
      }),
    onSuccess: (_d, isActive) => {
      toast.success(isActive ? `${u.username} enabled` : `${u.username} disabled`);
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      setConfirmDisable(false);
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Update failed"),
  });
  const pending = (u.groups ?? []).includes("kuso-pending");
  // Instance role: the user's default access level across the instance.
  // "admin" sees every project; editor/viewer set the baseline on
  // granted projects; "" inherits from the user's groups. Server
  // reissues the user's tokens on change so it takes effect on their
  // next request.
  const setInstanceRole = useMutation({
    mutationFn: (role: string) =>
      api(`/api/users/${encodeURIComponent(u.id)}/instance-role`, {
        method: "PUT",
        body: { role },
      }),
    onSuccess: (_d, role) => {
      toast.success(
        role
          ? `${u.username} is now ${role}`
          : `${u.username} now inherits from groups`
      );
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Role change failed"),
  });
  const groups = u.groups ?? [];
  const visibleGroups = groups.filter((g) => g !== "kuso-pending");
  return (
    <div role="row" className={cn("grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-3 px-4 py-3", USER_GRID)}>
      <div role="cell" className="flex min-w-0 items-center gap-3">
        <span className="relative shrink-0">
          <UserAvatar user={{ username: u.username }} size={32} />
          <span
            className={cn(
              "absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full border-2 border-[var(--bg-secondary)]",
              u.isActive ? "bg-[var(--success)]" : "bg-[var(--text-tertiary)]",
            )}
            title={u.isActive ? "Active" : "Disabled"}
          />
        </span>
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className={cn("truncate text-sm font-medium", u.isActive ? "text-[var(--text-primary)]" : "text-[var(--text-secondary)]")}>
              {u.username}
            </span>
            {!u.isActive && <Tag>Disabled</Tag>}
            {u.provider && u.provider !== "local" && (
              <Tag className="capitalize">{u.provider === "github" ? "GitHub" : u.provider}</Tag>
            )}
            {pending && (
              <Tag
                tone="warning"
                title="Joined through an invite with no group. Add them to a group or set an instance role to give them access."
              >
                Pending
              </Tag>
            )}
            {u.role && u.role !== "none" && (
              <Tag tone="accent" className="capitalize" title="Legacy role">
                {u.role}
              </Tag>
            )}
          </div>
          {u.email && <div className="truncate text-xs text-[var(--text-secondary)]">{u.email}</div>}
        </div>
      </div>

      {/* Mobile: actions sit top-right; desktop: they're the last column. */}
      <div role="cell" className="col-start-2 row-start-1 flex items-center justify-end gap-1 md:col-start-auto md:row-start-auto md:order-last">
        {canWrite && (
          <>
            <span className="mr-1 inline-flex" title={u.isActive ? "Active — click to disable" : "Disabled — click to enable"}>
              <Switch
                on={u.isActive}
                onChange={() => (u.isActive ? setConfirmDisable(true) : toggleActive.mutate(true))}
                disabled={toggleActive.isPending}
                label={u.isActive ? `Disable ${u.username}` : `Enable ${u.username}`}
              />
            </span>
            <Button variant="ghost" size="icon-sm" onClick={onReset} aria-label={`Reset password for ${u.username}`} title="Reset password">
              <KeyRound className="h-3.5 w-3.5" />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`Delete ${u.username}`}
              title="Delete user"
              onClick={() => setConfirmOpen(true)}
              className="hover:text-[var(--error)]"
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </>
        )}
      </div>

      <div role="cell" className="col-span-2 md:col-span-1">
        <span className="mb-1 block text-xs text-[var(--text-secondary)] md:hidden">Instance role</span>
        {canWrite ? (
          <SettingsSelect
            aria-label={`Instance role for ${u.username}`}
            title="Admin sees every project; editor/viewer set the default on granted projects; inherit uses the user's groups."
            value={u.instanceRole ?? ""}
            disabled={setInstanceRole.isPending}
            onChange={(e) => setInstanceRole.mutate(e.target.value)}
            className="h-8"
          >
            <option value="">Inherit from groups</option>
            <option value="admin">Admin</option>
            <option value="editor">Editor</option>
            <option value="viewer">Viewer</option>
          </SettingsSelect>
        ) : (
          <span className="text-sm capitalize text-[var(--text-primary)]">{u.instanceRole || "Inherit"}</span>
        )}
      </div>

      <div role="cell" className="min-w-0 text-sm text-[var(--text-secondary)]">
        <span className="mb-1 block text-xs md:hidden">Groups</span>
        {visibleGroups.length === 0 ? (
          <span className="text-[var(--text-tertiary)]">None</span>
        ) : (
          <span className="flex flex-wrap gap-1" title={visibleGroups.join(", ")}>
            {visibleGroups.slice(0, 3).map((g) => (
              <Tag key={g}>{g}</Tag>
            ))}
            {visibleGroups.length > 3 && <Tag>+{visibleGroups.length - 3}</Tag>}
          </span>
        )}
      </div>

      <div role="cell" className="text-sm text-[var(--text-secondary)]">
        <span className="mb-1 block text-xs md:hidden">Last login</span>
        {u.lastLogin ? (
          <span title={new Date(u.lastLogin).toLocaleString()} className="whitespace-nowrap">
            {relativeTime(u.lastLogin)}
          </span>
        ) : (
          <span className="text-[var(--text-tertiary)]">Never</span>
        )}
      </div>

      {canWrite && (
        <>
          <ConfirmDialog
            open={confirmDisable}
            title={`Disable ${u.username}?`}
            body={
              <p>
                {u.username} can no longer sign in until you enable the account
                again. Nothing is deleted.
              </p>
            }
            confirmLabel="Disable user"
            destructive
            pending={toggleActive.isPending}
            onConfirm={() => toggleActive.mutate(false)}
            onCancel={() => setConfirmDisable(false)}
          />
          <ConfirmDialog
            open={confirmOpen}
            title={`Delete ${u.username}?`}
            body={
              <p>
                Permanently removes the account, group memberships, and any
                tokens issued to this user. The user's projects are not
                touched. This cannot be undone.
              </p>
            }
            typeToConfirm={u.username}
            confirmLabel="Delete user"
            destructive
            pending={remove.isPending}
            onConfirm={() => remove.mutate()}
            onCancel={() => setConfirmOpen(false)}
          />
        </>
      )}
    </div>
  );
}

function Tag({
  children,
  tone = "neutral",
  className,
  title,
}: {
  children: React.ReactNode;
  tone?: "neutral" | "warning" | "accent";
  className?: string;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={cn(
        "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs leading-none",
        tone === "warning"
          ? "bg-[var(--warning-subtle)] text-[var(--warning)]"
          : tone === "accent"
            ? "bg-[var(--accent-subtle)] text-[var(--text-primary)]"
            : "bg-[var(--bg-tertiary)] text-[var(--text-secondary)]",
        className,
      )}
    >
      {children}
    </span>
  );
}

function CreateUserDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const create = useMutation({
    mutationFn: () =>
      api("/api/users", {
        method: "POST",
        body: { username, email, password, isActive: true },
      }),
    onSuccess: () => {
      toast.success(`User ${username} created`);
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Create failed"),
  });
  return (
    <Modal title="New user" onClose={onClose} busy={create.isPending}>
      <div className="space-y-4">
        <Field label="Username">
          <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" autoFocus />
        </Field>
        <Field label="Email">
          <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="off" />
        </Field>
        <Field label="Password" hint="At least 8 characters.">
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
        </Field>
      </div>
      <footer className="mt-4 flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onClose} disabled={create.isPending}>Cancel</Button>
        <Button size="sm" onClick={() => create.mutate()} disabled={create.isPending || !username || !email.trim() || !password || password.length < 8}>
          {create.isPending ? "Creating…" : "Create"}
        </Button>
      </footer>
    </Modal>
  );
}

function ResetPasswordDialog({
  userId,
  username,
  onClose,
}: {
  userId: string;
  username: string;
  onClose: () => void;
}) {
  const [password, setPassword] = useState("");
  const reset = useMutation({
    mutationFn: () => resetUserPassword(userId, password),
    onSuccess: () => {
      toast.success(`${username}'s password reset`);
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Reset failed"),
  });
  return (
    <Modal title={`Reset password for ${username}`} onClose={onClose} busy={reset.isPending}>
      <Field label="New password" hint="At least 8 characters. The user gets no email, so share it with them yourself.">
        <Input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="new-password"
          autoFocus
        />
      </Field>
      <footer className="mt-4 flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onClose} disabled={reset.isPending}>Cancel</Button>
        <Button size="sm" onClick={() => reset.mutate()} disabled={reset.isPending || password.length < 8}>
          {reset.isPending ? "Resetting…" : "Reset password"}
        </Button>
      </footer>
    </Modal>
  );
}

// Rendered only while open (callers mount it conditionally). `busy`
// blocks Escape/backdrop close while a request is in flight.
function Modal({
  title,
  onClose,
  busy = false,
  children,
}: {
  title: string;
  onClose: () => void;
  busy?: boolean;
  children: React.ReactNode;
}) {
  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next && !busy) onClose();
      }}
      disablePointerDismissal={busy}
    >
      <DialogContent className="block gap-0 rounded-md p-0 sm:max-w-md" showCloseButton={!busy}>
        <DialogHeader className="border-b border-[var(--border-subtle)] px-4 py-3 pr-10">
          <DialogTitle className="text-base">{title}</DialogTitle>
        </DialogHeader>
        <div className="p-4">{children}</div>
      </DialogContent>
    </Dialog>
  );
}

// A <label> wrapper so the control inside is named by the label text.
function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="block text-sm font-medium text-[var(--text-primary)]">{label}</span>
      {hint && <span className="block text-xs leading-relaxed text-[var(--text-secondary)]">{hint}</span>}
      {children}
    </label>
  );
}
