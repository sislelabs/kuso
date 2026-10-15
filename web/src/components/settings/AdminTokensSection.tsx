"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, Copy, Plus, ShieldCheck, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import {
  adminTokensQueryKey,
  issueTokenForUser,
  listAllTokens,
  listTokenUserOptions,
  revokeAnyToken,
  tokenUserOptionsQueryKey,
  type AdminTokenRow,
  type IssuedToken,
} from "@/features/admin-tokens";
import { relativeTime } from "@/lib/format";

// The server clamps every token to 365 days, admin-issued ones included.
const EXPIRY_DAYS = [30, 90, 365] as const;

const selectCls =
  "h-8 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]";
const labelCls = "font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]";

// AdminTokensSection lists every user's API tokens and lets a user:write
// holder mint one for someone else or revoke any. The caller gates render.
export function AdminTokensSection() {
  const qc = useQueryClient();
  const tokens = useQuery({ queryKey: adminTokensQueryKey, queryFn: listAllTokens });
  const users = useQuery({ queryKey: tokenUserOptionsQueryKey, queryFn: listTokenUserOptions });

  const [filterUser, setFilterUser] = useState("");
  const [userId, setUserId] = useState("");
  const [name, setName] = useState("");
  const [days, setDays] = useState<(typeof EXPIRY_DAYS)[number]>(90);
  const [issued, setIssued] = useState<(IssuedToken & { username: string }) | null>(null);
  const [copied, setCopied] = useState(false);
  const [revoking, setRevoking] = useState<AdminTokenRow | null>(null);

  const invalidate = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: adminTokensQueryKey }),
      // The caller's own token list may include the row just touched.
      qc.invalidateQueries({ queryKey: ["tokens", "my"] }),
    ]);

  const issue = useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: (v: { userId: string; name: string; expiresAt: string }) =>
      issueTokenForUser(v.userId, v.name, v.expiresAt),
    onSuccess: (r) => {
      const who = users.data?.find((u) => u.id === userId)?.username ?? userId;
      setIssued({ ...r, username: who });
      setName("");
      void invalidate();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Failed to issue token"),
  });

  const revoke = useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: (id: string) => revokeAnyToken(id),
    onSuccess: () => {
      toast.success("Token revoked");
      void invalidate();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Revoke failed"),
    onSettled: () => setRevoking(null),
  });

  const rows = useMemo(
    () => (tokens.data ?? []).filter((t) => !filterUser || t.user.id === filterUser),
    [tokens.data, filterUser]
  );

  const onIssue = (e: React.FormEvent) => {
    e.preventDefault();
    if (!userId) {
      toast.error("Pick the user the token is for");
      return;
    }
    if (!name.trim()) {
      toast.error("Name the token");
      return;
    }
    issue.mutate({
      userId,
      name: name.trim(),
      expiresAt: new Date(Date.now() + days * 86400_000).toISOString(),
    });
  };

  const copy = async (s: string) => {
    try {
      await navigator.clipboard.writeText(s);
    } catch {
      toast.error("Clipboard unavailable — select the token and copy it manually");
      return;
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 1200);
  };

  return (
    <section className="space-y-4 border-t border-[var(--border-subtle)] pt-6">
      <header className="flex items-center gap-3">
        <ShieldCheck className="h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h2 className="font-heading text-lg font-semibold tracking-tight">All users&apos; tokens</h2>
          <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
            Admin view (user:write). A token carries its user&apos;s permissions as of when it was
            issued; revoke and re-issue after changing someone&apos;s role.
          </p>
        </div>
      </header>

      <form onSubmit={onIssue} className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <div className="grid gap-3 p-4 sm:grid-cols-2">
          <div className="space-y-1">
            <label htmlFor="admin-token-user" className={labelCls}>user</label>
            {users.isError ? (
              <p className="font-mono text-[10px] text-[var(--error)]">
                Couldn&apos;t load users: {users.error instanceof Error ? users.error.message : "unknown error"}
              </p>
            ) : (
              <select
                id="admin-token-user"
                value={userId}
                onChange={(e) => setUserId(e.target.value)}
                disabled={users.isPending}
                className={selectCls}
              >
                <option value="">{users.isPending ? "loading…" : "pick a user"}</option>
                {(users.data ?? []).map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.username} {u.email ? `(${u.email})` : ""}
                  </option>
                ))}
              </select>
            )}
          </div>
          <div className="space-y-1">
            <label htmlFor="admin-token-name" className={labelCls}>name</label>
            <Input
              id="admin-token-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="ci deploy"
              className="h-8 font-mono text-[12px]"
            />
          </div>
          <div className="space-y-1">
            <span className={labelCls}>expires in</span>
            <div className="flex gap-1">
              {EXPIRY_DAYS.map((d) => (
                <button
                  key={d}
                  type="button"
                  onClick={() => setDays(d)}
                  className={
                    "inline-flex h-7 items-center rounded-md border px-2 font-mono text-[11px] transition-colors " +
                    (d === days
                      ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                      : "border-[var(--border-subtle)] bg-[var(--bg-primary)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)]")
                  }
                >
                  {d === 365 ? "1 year" : `${d} days`}
                </button>
              ))}
            </div>
          </div>
        </div>
        <footer className="flex justify-end border-t border-[var(--border-subtle)] px-4 py-3">
          <Button size="sm" type="submit" disabled={issue.isPending}>
            <Plus className="h-3 w-3" />
            {issue.isPending ? "Issuing…" : "Issue for user"}
          </Button>
        </footer>
      </form>

      {issued && (
        <section className="rounded-md border border-[var(--accent)]/40 bg-[var(--accent-subtle)] p-4">
          <h3 className="text-sm font-semibold">Token issued for {issued.username}</h3>
          <p className="mt-1 text-xs text-[var(--text-secondary)]">
            Copy it now and hand it over securely. kuso won&apos;t show it again.
          </p>
          <div className="mt-3 flex items-stretch gap-2">
            <code className="flex-1 break-all rounded border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1.5 font-mono text-[11px]">
              {issued.token}
            </code>
            <Button variant="outline" size="sm" type="button" onClick={() => void copy(issued.token)}>
              {copied ? <Check className="h-3 w-3 text-[var(--success)]" /> : <Copy className="h-3 w-3" />}
              {copied ? "Copied" : "Copy"}
            </Button>
          </div>
          <div className="mt-2 flex items-center justify-between">
            <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
              expires {new Date(issued.expiresAt).toLocaleDateString()}
            </span>
            <Button variant="ghost" size="sm" type="button" onClick={() => setIssued(null)}>
              dismiss
            </Button>
          </div>
        </section>
      )}

      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <header className="flex items-center justify-between gap-3 border-b border-[var(--border-subtle)] px-4 py-2.5">
          <h3 className="text-sm font-semibold tracking-tight">Issued tokens</h3>
          <select
            aria-label="Filter by user"
            value={filterUser}
            onChange={(e) => setFilterUser(e.target.value)}
            className="h-7 max-w-[200px] rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px]"
          >
            <option value="">all users</option>
            {(users.data ?? []).map((u) => (
              <option key={u.id} value={u.id}>{u.username}</option>
            ))}
          </select>
        </header>
        {tokens.isPending ? (
          <Skeleton className="m-3 h-16" />
        ) : tokens.isError ? (
          <QueryErrorState what="tokens" error={tokens.error} onRetry={() => void tokens.refetch()} className="m-3" />
        ) : rows.length === 0 ? (
          <p className="px-4 py-4 text-[11px] text-[var(--text-tertiary)]">
            {filterUser ? "This user has no tokens." : "No tokens issued on this instance."}
          </p>
        ) : (
          <ul>
            {rows.map((t) => {
              const expired = new Date(t.expiresAt).getTime() < Date.now();
              return (
                <li
                  key={t.id}
                  className="flex items-center gap-3 border-b border-[var(--border-subtle)] px-4 py-2.5 last:border-b-0"
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 truncate text-sm">
                      <span className="font-medium">{t.name || "(unnamed)"}</span>
                      <span className="font-mono text-[11px] text-[var(--text-secondary)]">{t.user.username}</span>
                      {(expired || !t.isActive) && (
                        <span className="rounded bg-[var(--bg-tertiary)] px-1 font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
                          {expired ? "expired" : "inactive"}
                        </span>
                      )}
                    </div>
                    <p className="mt-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
                      issued {relativeTime(t.createdAt)} · expires {new Date(t.expiresAt).toLocaleDateString()}
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    type="button"
                    aria-label={`Revoke ${t.name} for ${t.user.username}`}
                    onClick={() => setRevoking(t)}
                    disabled={revoke.isPending}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </li>
              );
            })}
          </ul>
        )}
      </section>

      <ConfirmDialog
        open={revoking !== null}
        title={`Revoke ${revoking?.name || "token"}?`}
        body={
          <p>
            Belongs to <span className="font-mono text-[var(--text-primary)]">{revoking?.user.username}</span>.
            Anything using it (CLI logins, scripts, CI) stops working immediately.
          </p>
        }
        confirmLabel="Revoke token"
        destructive
        pending={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
        onCancel={() => setRevoking(null)}
      />
    </section>
  );
}
