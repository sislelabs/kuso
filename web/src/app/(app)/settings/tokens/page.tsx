"use client";

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
import { Plus, Trash2, Copy, Check, Infinity, KeyRound } from "lucide-react";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

// Preset expiry choices. "Never" sends an empty expiresAt; the
// server treats that as "infinite" (omits the JWT exp claim, stores a
// 100y sentinel in the row). Custom keeps the numeric input around
// for the rare "1.5 year" case without cluttering the common path.
const PRESETS: { label: string; days: number | null }[] = [
  { label: "30 days",  days: 30 },
  { label: "90 days",  days: 90 },
  { label: "1 year",   days: 365 },
  { label: "Never",    days: null },
  { label: "Custom",   days: 0 },
];

export default function TokensPage() {
  const qc = useQueryClient();
  const tokens = useQuery({ queryKey: ["tokens", "my"], queryFn: listMyTokens });
  const issue = useMutation({
    mutationFn: ({ name, expiresAt }: { name: string; expiresAt: string }) =>
      issueMyToken(name, expiresAt),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens", "my"] }),
  });
  const revoke = useMutation({
    mutationFn: (id: string) => revokeMyToken(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens", "my"] }),
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
    if (p.days === null) return ""; // never → empty → server omits exp
    const days = p.days === 0 ? customDays : p.days;
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
      toast.error(err instanceof Error ? err.message : "Failed to issue token");
    }
  };

  const copy = (s: string) => {
    navigator.clipboard.writeText(s);
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
    <div className="mx-auto max-w-2xl p-6 lg:p-8 space-y-6">
      <header className="flex items-center gap-3">
        <KeyRound className="h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">
            Personal access tokens
          </h1>
          <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
            Use in <span className="font-mono">kuso login --token</span> or pass as{" "}
            <span className="font-mono">Authorization: Bearer &lt;token&gt;</span> to the API.
          </p>
        </div>
      </header>

      <form
        onSubmit={onIssue}
        className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
      >
        <div className="space-y-3 p-4">
          <Field label="name" hint="memo so you remember which device it lives on">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my laptop"
              className="h-8 font-mono text-[12px]"
              autoFocus
              required
            />
          </Field>
          <Field label="expires" hint="when the token stops working">
            <div className="flex flex-wrap gap-1">
              {PRESETS.map((p) => {
                const active = p.label === preset;
                return (
                  <button
                    key={p.label}
                    type="button"
                    onClick={() => setPreset(p.label)}
                    className={cn(
                      "inline-flex h-7 items-center gap-1 rounded-md border px-2 font-mono text-[11px] transition-colors",
                      active
                        ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                        : "border-[var(--border-subtle)] bg-[var(--bg-primary)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
                    )}
                  >
                    {p.days === null && <Infinity className="h-3 w-3" />}
                    {p.label}
                  </button>
                );
              })}
            </div>
            {preset === "Custom" && (
              <div className="mt-2 inline-flex items-center gap-1.5">
                <Input
                  type="number"
                  min={1}
                  max={365 * 50}
                  value={customDays}
                  onChange={(e) => setCustomDays(parseInt(e.target.value, 10) || 30)}
                  className="h-7 w-24 font-mono text-[11px]"
                />
                <span className="font-mono text-[10px] text-[var(--text-tertiary)]">days</span>
              </div>
            )}
            {preset === "Never" && (
              <p className="mt-2 text-[10px] text-amber-400">
                ⚠ Non-expiring tokens stay valid until you manually revoke. Use sparingly —
                rotate when a laptop is lost.
              </p>
            )}
          </Field>
        </div>
        <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
          <Button size="sm" type="submit" disabled={issue.isPending}>
            <Plus className="h-3 w-3" />
            {issue.isPending ? "Issuing…" : "Issue token"}
          </Button>
        </footer>
      </form>

      {issued && (
        <section className="rounded-md border border-[var(--accent)]/40 bg-[var(--accent-subtle)] p-4">
          <h2 className="text-sm font-semibold">Token issued</h2>
          <p className="mt-1 text-xs text-[var(--text-secondary)]">
            Save this now — kuso never shows it again.
          </p>
          <div className="mt-3 flex items-stretch gap-2">
            <code className="flex-1 truncate rounded border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1.5 font-mono text-[11px]">
              {issued.token}
            </code>
            <Button variant="outline" size="sm" type="button" onClick={() => copy(issued.token)}>
              {copied ? <Check className="h-3 w-3 text-emerald-400" /> : <Copy className="h-3 w-3" />}
              {copied ? "Copied" : "Copy"}
            </Button>
          </div>
          <div className="mt-2 flex items-center justify-between">
            <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
              expires{" "}
              {isInfinite(issued.expiresAt)
                ? "never"
                : new Date(issued.expiresAt).toLocaleDateString()}
            </span>
            <Button variant="ghost" size="sm" type="button" onClick={() => setIssued(null)}>
              dismiss
            </Button>
          </div>
        </section>
      )}

      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-2.5">
          <h2 className="text-sm font-semibold tracking-tight">Existing tokens</h2>
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            {tokens.data?.length ?? 0} {(tokens.data?.length ?? 0) === 1 ? "token" : "tokens"}
          </span>
        </header>
        {tokens.isPending ? (
          <Skeleton className="m-3 h-16" />
        ) : (tokens.data ?? []).length === 0 ? (
          <p className="px-4 py-4 text-[11px] text-[var(--text-tertiary)]">
            No tokens issued yet.
          </p>
        ) : (
          <ul>
            {tokens.data!.map((t) => (
              <li
                key={t.id}
                className="flex items-center gap-3 border-b border-[var(--border-subtle)] px-4 py-2.5 last:border-b-0"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5 truncate text-sm font-medium">
                    {t.name}
                    {isInfinite(t.expiresAt) && (
                      <span
                        className="inline-flex items-center gap-0.5 rounded bg-amber-500/10 px-1 py-0.5 font-mono text-[9px] uppercase tracking-widest text-amber-400"
                        title="Never expires"
                      >
                        <Infinity className="h-2 w-2" />
                        ∞
                      </span>
                    )}
                  </div>
                  <p className="mt-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
                    issued {relativeTime(t.createdAt)} ·{" "}
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
                  aria-label="Revoke"
                  onClick={() => revoke.mutate(t.id)}
                  disabled={revoke.isPending}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        {label}
      </div>
      {children}
      {hint && <div className="text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>}
    </div>
  );
}
