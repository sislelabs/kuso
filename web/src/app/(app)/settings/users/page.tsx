"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import { toast } from "sonner";
import { Users as UsersIcon, Plus, Trash2, KeyRound, X, Link2, Copy, Check } from "lucide-react";
import { cn } from "@/lib/utils";
import { relativeTime } from "@/lib/format";

interface UserRow {
  id: string;
  username: string;
  email?: string;
  isActive: boolean;
  provider?: string;
  roleName?: string;
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

  return (
    <div className="mx-auto max-w-3xl p-6 lg:p-8">
      <header className="mb-6 flex items-center gap-3">
        <UsersIcon className="h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">Users</h1>
          <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
            Local accounts + OAuth-created users. Roles + group memberships set the perms.
          </p>
        </div>
      </header>

      {!canWrite && (
        <p className="mb-4 rounded-md border border-amber-500/30 bg-amber-500/5 p-3 font-mono text-[10px] text-amber-400">
          You don&apos;t have <span className="text-[var(--text-secondary)]">user:write</span>.
          The page is read-only.
        </p>
      )}

      <div className="mb-3 flex items-center justify-end">
        {canWrite && (
          <Button size="sm" onClick={() => setShowCreate(true)}>
            <Plus className="h-3 w-3" />
            New user
          </Button>
        )}
      </div>

      {list.isPending ? (
        <Skeleton className="h-32 rounded-md" />
      ) : (list.data ?? []).length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-8 text-center text-sm text-[var(--text-tertiary)]">
          No users.
        </p>
      ) : (
        <ul className="space-y-2">
          {(list.data ?? []).map((u) => (
            <li key={u.id}>
              <UserRowItem
                u={u}
                canWrite={canWrite}
                onReset={() => setResetting(u.id)}
              />
            </li>
          ))}
        </ul>
      )}

      {canWrite && <InvitesSection />}

      {showCreate && <CreateUserDialog onClose={() => setShowCreate(false)} />}
      {resetting && (
        <ResetPasswordDialog
          userId={resetting}
          username={(list.data ?? []).find((u) => u.id === resetting)?.username ?? ""}
          onClose={() => setResetting(null)}
        />
      )}
    </div>
  );
}

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

  const revoke = useMutation({
    mutationFn: (id: string) => api(`/api/invites/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Invite revoked");
      qc.invalidateQueries({ queryKey: ["admin", "invites"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Revoke failed"),
  });

  const items = list.data ?? [];

  return (
    <section className="mt-10">
      <header className="mb-3 flex items-center justify-between">
        <div>
          <h2 className="font-heading text-base font-semibold tracking-tight">Invitations</h2>
          <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
            Generate links that let people sign up directly into a group.
          </p>
        </div>
        <Button size="sm" onClick={() => setShowNew(true)}>
          <Plus className="h-3 w-3" />
          New invite
        </Button>
      </header>

      {list.isPending ? (
        <Skeleton className="h-24 rounded-md" />
      ) : items.length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-xs text-[var(--text-tertiary)]">
          No invites yet.
        </p>
      ) : (
        <ul className="space-y-2">
          {items.map((inv) => (
            <li key={inv.id}>
              <InviteRowItem inv={inv} onRevoke={() => revoke.mutate(inv.id)} />
            </li>
          ))}
        </ul>
      )}

      {showNew && <CreateInviteDialog onClose={() => setShowNew(false)} />}
    </section>
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
      toast.error("clipboard unavailable");
    }
  };

  return (
    <div
      className={cn(
        "rounded-md border bg-[var(--bg-secondary)] p-3",
        dead ? "border-[var(--border-subtle)] opacity-60" : "border-[var(--border-subtle)]"
      )}
    >
      <div className="flex items-start gap-3">
        <Link2 className="mt-0.5 h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <code className="truncate rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-secondary)]">
              {inv.url}
            </code>
            <button
              type="button"
              onClick={copy}
              aria-label="Copy"
              className="inline-flex h-6 w-6 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
            >
              {copied ? (
                <Check className="h-3 w-3 text-emerald-500" />
              ) : (
                <Copy className="h-3 w-3" />
              )}
            </button>
          </div>
          <div className="flex flex-wrap items-center gap-2 font-mono text-[10px] text-[var(--text-tertiary)]">
            <span className={cn(
              "rounded px-1.5 py-0.5",
              dead ? "bg-[var(--bg-tertiary)]" : "bg-emerald-500/10 text-emerald-400"
            )}>
              {status}
            </span>
            <span>· uses {inv.usedCount}/{inv.maxUses}</span>
            {role && <span>· role {role}</span>}
            {nullTime(inv.expiresAt) && (
              <span title={nullTime(inv.expiresAt)}>
                · expires {relativeTime(nullTime(inv.expiresAt))}
              </span>
            )}
            <span title={inv.createdAt}>· created {relativeTime(inv.createdAt)}</span>
          </div>
          {note && (
            <p className="text-[11px] italic text-[var(--text-secondary)]">&ldquo;{note}&rdquo;</p>
          )}
        </div>
        {!dead && (
          <Button variant="outline" size="sm" onClick={onRevoke}>
            <Trash2 className="h-3 w-3" /> Revoke
          </Button>
        )}
      </div>
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
          expiresIn: expiresIn || undefined,
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
    <Modal title="New invitation" onClose={onClose}>
      <div className="space-y-3">
        <Field label="group" hint="invitee joins this group on signup; empty = pending">
          <select
            value={groupId}
            onChange={(e) => setGroupId(e.target.value)}
            className="h-8 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--input)] px-2 font-mono text-[12px]"
          >
            <option value="">— pending (admin approves later) —</option>
            {(groups.data ?? []).map((g) => (
              <option key={g.id} value={g.id}>
                {g.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="instance role" hint="override the group's default; empty = use group">
          <select
            value={instanceRole}
            onChange={(e) => setInstanceRole(e.target.value)}
            className="h-8 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--input)] px-2 font-mono text-[12px]"
          >
            <option value="">— use group default —</option>
            <option value="admin">admin</option>
            <option value="member">member</option>
            <option value="viewer">viewer</option>
            <option value="billing">billing</option>
            <option value="pending">pending</option>
          </select>
        </Field>
        <Field label="expires in" hint="duration like 24h, 168h (7d), 720h (30d). empty = never">
          <Input
            value={expiresIn}
            onChange={(e) => setExpiresIn(e.target.value)}
            className="h-8 font-mono text-[12px]"
            placeholder="168h"
          />
        </Field>
        <Field label="max uses" hint="multi-use links require an expiry">
          <Input
            type="number"
            min={1}
            max={1000}
            value={maxUses}
            onChange={(e) => setMaxUses(Number(e.target.value) || 1)}
            className="h-8 font-mono text-[12px]"
          />
        </Field>
        <Field label="note" hint="optional; shown to invitee on the signup page">
          <Input
            value={note}
            onChange={(e) => setNote(e.target.value)}
            className="h-8 text-[12px]"
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
  const remove = useMutation({
    mutationFn: () => api(`/api/users/id/${encodeURIComponent(u.id)}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success(`${u.username} deleted`);
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });
  const toggleActive = useMutation({
    mutationFn: (isActive: boolean) =>
      api(`/api/users/id/${encodeURIComponent(u.id)}`, {
        method: "PUT",
        body: { isActive },
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "users"] }),
  });
  return (
    <div className="flex items-center gap-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-4 py-3">
      <span
        className={cn(
          "inline-block h-2 w-2 shrink-0 rounded-full",
          u.isActive ? "bg-emerald-400" : "bg-[var(--text-tertiary)]/40"
        )}
        title={u.isActive ? "Active" : "Disabled"}
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2 truncate text-sm font-medium">
          {u.username}
          {u.provider && u.provider !== "local" && (
            <span className="rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
              {u.provider}
            </span>
          )}
          {u.roleName && u.roleName !== "none" && (
            <span className="rounded bg-[var(--accent-subtle)] px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-widest text-[var(--accent)]">
              {u.roleName}
            </span>
          )}
        </div>
        <div className="mt-0.5 flex items-center gap-2 font-mono text-[10px] text-[var(--text-tertiary)]">
          {u.email && <span className="truncate">{u.email}</span>}
          {u.lastLogin && (
            <span title={u.lastLogin}>· last login {relativeTime(u.lastLogin)}</span>
          )}
          {(u.groups ?? []).length > 0 && (
            <span className="truncate">· groups: {u.groups!.join(", ")}</span>
          )}
        </div>
      </div>
      {canWrite && (
        <>
          <button
            type="button"
            onClick={() => toggleActive.mutate(!u.isActive)}
            className={cn(
              "inline-flex h-6 w-10 shrink-0 items-center rounded-full border transition-colors",
              u.isActive
                ? "border-emerald-500/30 bg-emerald-500/20"
                : "border-[var(--border-subtle)] bg-[var(--bg-tertiary)]"
            )}
            aria-label={u.isActive ? "Disable" : "Enable"}
            title={u.isActive ? "Disable user" : "Enable user"}
          >
            <span
              className={cn(
                "inline-block h-4 w-4 rounded-full bg-white shadow transition-transform",
                u.isActive ? "translate-x-5" : "translate-x-1"
              )}
            />
          </button>
          <Button variant="outline" size="sm" onClick={onReset}>
            <KeyRound className="h-3 w-3" />
            Reset password
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Delete"
            onClick={() => {
              if (window.confirm(`Delete ${u.username}? This cannot be undone.`)) remove.mutate();
            }}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </>
      )}
    </div>
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
    <Modal title="New user" onClose={onClose}>
      <div className="space-y-3">
        <Field label="username">
          <Input value={username} onChange={(e) => setUsername(e.target.value)} className="h-8 font-mono text-[12px]" autoFocus />
        </Field>
        <Field label="email">
          <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} className="h-8 font-mono text-[12px]" />
        </Field>
        <Field label="password" hint="≥ 8 chars">
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} className="h-8 font-mono text-[12px]" />
        </Field>
      </div>
      <footer className="mt-4 flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onClose} disabled={create.isPending}>Cancel</Button>
        <Button size="sm" onClick={() => create.mutate()} disabled={create.isPending || !username || !password || password.length < 8}>
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
    mutationFn: () =>
      api(`/api/users/id/${encodeURIComponent(userId)}`, {
        method: "PUT",
        body: { password },
      }),
    onSuccess: () => {
      toast.success(`${username}'s password reset`);
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Reset failed"),
  });
  return (
    <Modal title={`Reset password for ${username}`} onClose={onClose}>
      <Field label="new password" hint="≥ 8 chars; user receives no email — share manually">
        <Input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          className="h-8 font-mono text-[12px]"
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

function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: React.ReactNode }) {
  return (
    <div
      className="fixed inset-0 z-[55] flex items-center justify-center bg-[rgba(8,8,11,0.6)] p-4"
      onClick={onClose}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-md rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] shadow-[var(--shadow-lg)]"
      >
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-3">
          <h2 className="text-sm font-semibold">{title}</h2>
          <button onClick={onClose} aria-label="Close" className="text-[var(--text-tertiary)] hover:text-[var(--text-primary)]">
            <X className="h-3.5 w-3.5" />
          </button>
        </header>
        <div className="p-4">{children}</div>
      </div>
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">{label}</div>
      {children}
      {hint && <div className="text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>}
    </div>
  );
}
