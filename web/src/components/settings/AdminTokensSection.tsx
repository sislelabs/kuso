"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, Copy, Plus, Trash2 } from "lucide-react";
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
import { cn } from "@/lib/utils";
import { SettingsField, SettingsSection, SettingsSelect } from "./SettingsSection";

// The server clamps every token to 365 days, admin-issued ones included.
const EXPIRY_DAYS = [30, 90, 365] as const;

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
    <>
      <div className="pt-4">
        <h2 className="font-heading text-lg font-semibold tracking-tight text-[var(--text-primary)]">
          All users&apos; tokens
        </h2>
        <p className="mt-1 max-w-2xl text-sm text-[var(--text-secondary)]">
          Admin view (user:write). A token carries its user&apos;s permissions as of when it was issued; revoke and
          re-issue after changing someone&apos;s role.
        </p>
      </div>

      <form onSubmit={onIssue}>
        <SettingsSection
          title="Issue for a user"
          footer={
            <Button size="sm" type="submit" disabled={issue.isPending}>
              <Plus className="h-3.5 w-3.5" />
              {issue.isPending ? "Issuing…" : "Issue for user"}
            </Button>
          }
        >
          <div className="grid grid-cols-1 gap-5 sm:grid-cols-2">
            <SettingsField
              label="User"
              htmlFor="admin-token-user"
              error={
                users.isError
                  ? `Couldn't load users: ${users.error instanceof Error ? users.error.message : "unknown error"}`
                  : undefined
              }
            >
              {!users.isError && (
                <SettingsSelect
                  id="admin-token-user"
                  value={userId}
                  onChange={(e) => setUserId(e.target.value)}
                  disabled={users.isPending}
                >
                  <option value="">{users.isPending ? "Loading…" : "Pick a user"}</option>
                  {(users.data ?? []).map((u) => (
                    <option key={u.id} value={u.id}>
                      {u.username} {u.email ? `(${u.email})` : ""}
                    </option>
                  ))}
                </SettingsSelect>
              )}
            </SettingsField>
            <SettingsField label="Name" htmlFor="admin-token-name">
              <Input
                id="admin-token-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. ci deploy"
              />
            </SettingsField>
          </div>
          <SettingsField label="Expires in">
            <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Expires in">
              {EXPIRY_DAYS.map((d) => (
                <button
                  key={d}
                  type="button"
                  role="radio"
                  aria-checked={d === days}
                  onClick={() => setDays(d)}
                  className={cn(
                    "inline-flex h-8 items-center rounded-sm border px-3 text-sm transition-colors",
                    d === days
                      ? "border-[var(--border-strong)] bg-[var(--bg-tertiary)] font-medium text-[var(--text-primary)]"
                      : "border-[var(--border-subtle)] bg-[var(--bg-primary)] text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
                  )}
                >
                  {d === 365 ? "1 year" : `${d} days`}
                </button>
              ))}
            </div>
          </SettingsField>
        </SettingsSection>
      </form>

      {issued && (
        <section className="rounded-md border border-[var(--success)]/40 bg-[var(--success-subtle)] p-4 sm:p-5">
          <h3 className="text-base font-semibold text-[var(--text-primary)]">Token issued for {issued.username}</h3>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Copy it now and hand it over securely. kuso won&apos;t show it again.
          </p>
          <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-stretch">
            <code className="flex-1 break-all rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2.5 py-2 font-mono text-xs text-[var(--text-primary)]">
              {issued.token}
            </code>
            <Button variant="outline" size="sm" type="button" onClick={() => void copy(issued.token)}>
              {copied ? <Check className="h-3.5 w-3.5 text-[var(--success)]" /> : <Copy className="h-3.5 w-3.5" />}
              {copied ? "Copied" : "Copy"}
            </Button>
          </div>
          <div className="mt-2 flex items-center justify-between">
            <span className="text-xs text-[var(--text-secondary)]">
              Expires {new Date(issued.expiresAt).toLocaleDateString()}
            </span>
            <Button variant="ghost" size="sm" type="button" onClick={() => setIssued(null)}>
              Dismiss
            </Button>
          </div>
        </section>
      )}

      <SettingsSection
        title="Issued tokens"
        actions={
          <SettingsSelect
            aria-label="Filter by user"
            value={filterUser}
            onChange={(e) => setFilterUser(e.target.value)}
            wrapperClassName="w-48"
            className="h-8"
          >
            <option value="">All users</option>
            {(users.data ?? []).map((u) => (
              <option key={u.id} value={u.id}>{u.username}</option>
            ))}
          </SettingsSelect>
        }
        flush
      >
        {tokens.isPending ? (
          <Skeleton className="m-4 h-16" />
        ) : tokens.isError ? (
          <QueryErrorState what="tokens" error={tokens.error} onRetry={() => void tokens.refetch()} className="m-4" />
        ) : rows.length === 0 ? (
          <p className="px-4 py-4 text-sm text-[var(--text-tertiary)] sm:px-5">
            {filterUser ? "This user has no tokens." : "No tokens issued on this instance."}
          </p>
        ) : (
          <ul className="divide-y divide-[var(--border-subtle)]">
            {rows.map((t) => {
              const expired = new Date(t.expiresAt).getTime() < Date.now();
              return (
                <li key={t.id} className="flex items-center gap-3 px-4 py-3 sm:px-5">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm">
                      <span className="font-medium text-[var(--text-primary)]">{t.name || "(unnamed)"}</span>
                      <span className="text-[var(--text-secondary)]">{t.user.username}</span>
                      {(expired || !t.isActive) && (
                        <span className="rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-xs text-[var(--text-tertiary)]">
                          {expired ? "Expired" : "Inactive"}
                        </span>
                      )}
                    </div>
                    <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
                      Issued {relativeTime(t.createdAt)} · expires {new Date(t.expiresAt).toLocaleDateString()}
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    type="button"
                    title="Revoke"
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
      </SettingsSection>

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
        typeToConfirm={revoking?.name || undefined}
        pending={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
        onCancel={() => setRevoking(null)}
      />
    </>
  );
}
