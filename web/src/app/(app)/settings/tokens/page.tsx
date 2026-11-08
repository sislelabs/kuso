"use client";

import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  issueMyToken,
  listMyTokens,
  revokeMyToken,
  type IssueTokenResponse,
} from "@/features/profile/api";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { Plus, Trash2, Copy, Check, Infinity as InfinityIcon } from "lucide-react";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsField, SettingsSection } from "@/components/settings/SettingsSection";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { AdminTokensSection } from "@/components/settings/AdminTokensSection";
import { useCan, Perms } from "@/features/auth";

// Preset expiry choices. There is no "Never": the server clamps every
// personal token to 365 days (baked-in perms must not outlive a demotion),
// so offering it would mint a 1-year token labelled as non-expiring.
const MAX_TOKEN_DAYS = 365;
const PRESETS: { label: string; days: number }[] = [
  { label: "30 days",  days: 30 },
  { label: "90 days",  days: 90 },
  { label: "1 year",   days: MAX_TOKEN_DAYS },
  { label: "Custom",   days: 0 },
];

export default function TokensPage() {
  const qc = useQueryClient();
  const canManageAll = useCan(Perms.UserWrite);
  const tokens = useQuery({ queryKey: ["tokens", "my"], queryFn: listMyTokens });
  const issue = useMutation({
    // Handled by the caller's try/catch toast — opt out of the global
    // mutation-error toast to avoid doubling.
    meta: { skipGlobalErrorToast: true },
    mutationFn: ({ name, expiresAt }: { name: string; expiresAt: string }) =>
      issueMyToken(name, expiresAt),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens"] }),
  });
  const [revoking, setRevoking] = useState<{ id: string; name: string } | null>(null);
  const revoke = useMutation({
    mutationFn: (id: string) => revokeMyToken(id),
    onSuccess: () => {
      setRevoking(null);
      qc.invalidateQueries({ queryKey: ["tokens"] });
    },
  });

  const [name, setName] = useState("");
  const [preset, setPreset] = useState<string>("30 days");
  const [customDays, setCustomDays] = useState(30);
  const [issued, setIssued] = useState<IssueTokenResponse | null>(null);
  const [copied, setCopied] = useState(false);

  // Resolve the picked preset into the wire shape.
  const expiresAtFor = (): string => {
    const p = PRESETS.find((x) => x.label === preset);
    if (!p) return "";
    const days = Math.min(p.days === 0 ? customDays : p.days, MAX_TOKEN_DAYS);
    return new Date(Date.now() + days * 86400_000).toISOString();
  };

  const onIssue = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      toast.error("Name your token (e.g. 'my laptop')");
      return;
    }
    try {
      const r = await issue.mutateAsync({ name: name.trim(), expiresAt: expiresAtFor() });
      setIssued(r);
      setName("");
    } catch (err) {
      toastError(err, "Failed to issue token");
    }
  };

  const copy = async (s: string) => {
    try {
      await navigator.clipboard.writeText(s);
    } catch {
      toast.error("Clipboard unavailable — select the token and copy it manually");
      return;
    }
    setCopied(true);
    toast.success("Copied to clipboard");
    setTimeout(() => setCopied(false), 1200);
  };

  // A token is "infinite" when its persisted expiresAt is past the
  // 90y mark (server uses 100y as a sentinel). Cheap probabilistic
  // check that doesn't need the row to carry an explicit isInfinite
  // bit through the API.
  const isInfinite = (iso: string) => {
    const t = new Date(iso).getTime();
    return Number.isFinite(t) && t - Date.now() > 80 * 365 * 86400_000;
  };

  return (
    <SettingsPage
      title="API tokens"
      description={
        <>
          Personal access tokens for the CLI and scripts. Use with{" "}
          <span className="font-mono text-[var(--text-primary)]">kuso login --token</span> or send as{" "}
          <span className="font-mono text-[var(--text-primary)]">Authorization: Bearer &lt;token&gt;</span>.
        </>
      }
    >
      <form onSubmit={onIssue}>
        <SettingsSection
          title="New token"
          description="Tokens carry your current permissions and stop working when they expire."
          footerHint={`Tokens last at most ${MAX_TOKEN_DAYS} days.`}
          footer={
            <Button size="sm" type="submit" disabled={issue.isPending}>
              <Plus className="h-3.5 w-3.5" />
              {issue.isPending ? "Issuing…" : "Issue token"}
            </Button>
          }
        >
          <SettingsField label="Name" htmlFor="token-name" hint="A memo so you remember which device or script uses it.">
            <Input
              id="token-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. my laptop"
              autoFocus
              required
            />
          </SettingsField>
          <SettingsField label="Expires" hint="When the token stops working.">
            <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Expiry">
              {PRESETS.map((p) => {
                const active = p.label === preset;
                return (
                  <button
                    key={p.label}
                    type="button"
                    role="radio"
                    aria-checked={active}
                    onClick={() => setPreset(p.label)}
                    className={cn(
                      "inline-flex h-8 items-center rounded-sm border px-3 text-sm transition-colors",
                      active
                        ? "border-[var(--border-strong)] bg-[var(--bg-tertiary)] font-medium text-[var(--text-primary)]"
                        : "border-[var(--border-subtle)] bg-[var(--bg-primary)] text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
                    )}
                  >
                    {p.label}
                  </button>
                );
              })}
            </div>
            {preset === "Custom" && (
              <div className="mt-2 flex items-center gap-2">
                <Input
                  type="number"
                  min={1}
                  max={MAX_TOKEN_DAYS}
                  value={customDays}
                  onChange={(e) => setCustomDays(parseInt(e.target.value, 10) || 30)}
                  aria-label="Custom expiry in days"
                  className="w-28 font-mono"
                />
                <span className="text-xs text-[var(--text-secondary)]">days (max {MAX_TOKEN_DAYS})</span>
              </div>
            )}
          </SettingsField>
        </SettingsSection>
      </form>

      {issued && (
        <section className="rounded-md border border-[var(--success)]/40 bg-[var(--success-subtle)] p-4 sm:p-5">
          <h2 className="text-base font-semibold text-[var(--text-primary)]">Token issued</h2>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">Copy it now. kuso never shows it again.</p>
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
              Expires{" "}
              {isInfinite(issued.expiresAt) ? "never" : new Date(issued.expiresAt).toLocaleDateString()}
            </span>
            <Button variant="ghost" size="sm" type="button" onClick={() => setIssued(null)}>
              Dismiss
            </Button>
          </div>
        </section>
      )}

      <SettingsSection
        title="Your tokens"
        description={`${tokens.data?.length ?? 0} ${(tokens.data?.length ?? 0) === 1 ? "token" : "tokens"}`}
        flush
      >
        {tokens.isPending ? (
          <Skeleton className="m-4 h-16" />
        ) : tokens.isError ? (
          <QueryErrorState what="tokens" error={tokens.error} onRetry={() => void tokens.refetch()} className="m-4" />
        ) : (tokens.data ?? []).length === 0 ? (
          <p className="px-4 py-4 text-sm text-[var(--text-tertiary)] sm:px-5">No tokens issued yet.</p>
        ) : (
          <ul className="divide-y divide-[var(--border-subtle)]">
            {tokens.data!.map((t) => (
              <li key={t.id} className="flex items-center gap-3 px-4 py-3 sm:px-5">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5 text-sm font-medium text-[var(--text-primary)]">
                    <span className="truncate">{t.name}</span>
                    {isInfinite(t.expiresAt) && (
                      <span
                        className="inline-flex items-center gap-1 rounded bg-[var(--warning-subtle)] px-1.5 py-0.5 text-xs text-[var(--warning)]"
                        title="Never expires"
                      >
                        <InfinityIcon className="h-3 w-3" />
                        No expiry
                      </span>
                    )}
                  </div>
                  <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
                    Issued {relativeTime(t.createdAt)} ·{" "}
                    {isInfinite(t.expiresAt) ? (
                      <span>does not expire</span>
                    ) : (
                      <>expires {new Date(t.expiresAt).toLocaleDateString()}</>
                    )}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  type="button"
                  aria-label={`Revoke ${t.name}`}
                  title="Revoke"
                  onClick={() => setRevoking({ id: t.id, name: t.name })}
                  disabled={revoke.isPending}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </SettingsSection>

      <ConfirmDialog
        open={revoking !== null}
        title={`Revoke ${revoking?.name ?? "token"}?`}
        body={<p>Anything using this token (CLI logins, scripts, CI) stops working immediately.</p>}
        confirmLabel="Revoke token"
        destructive
        typeToConfirm={revoking?.name || undefined}
        pending={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
        onCancel={() => setRevoking(null)}
      />

      {canManageAll && <AdminTokensSection />}
    </SettingsPage>
  );
}
