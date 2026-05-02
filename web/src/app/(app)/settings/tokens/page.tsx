"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  issueMyToken,
  listMyTokens,
  revokeMyToken,
  type IssueTokenResponse,
} from "@/features/profile/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { toast } from "sonner";
import { Plus, Trash2, Copy } from "lucide-react";
import { relativeTime } from "@/lib/format";

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
  const [days, setDays] = useState(30);
  const [issued, setIssued] = useState<IssueTokenResponse | null>(null);

  const onIssue = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name) return;
    const expiresAt = new Date(Date.now() + days * 86400_000).toISOString();
    try {
      const r = await issue.mutateAsync({ name, expiresAt });
      setIssued(r);
      setName("");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to issue token");
    }
  };

  const copy = (s: string) => {
    navigator.clipboard.writeText(s);
    toast.success("Copied to clipboard");
  };

  return (
    <div className="mx-auto max-w-2xl p-6 lg:p-8 space-y-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold tracking-tight">
          Personal access tokens
        </h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          Use these in <span className="font-mono">kuso login --token</span> or pass
          as <span className="font-mono">Authorization: Bearer &lt;token&gt;</span>
          to the API.
        </p>
      </div>

      <form onSubmit={onIssue}>
        <Card>
          <CardHeader>
            <CardTitle>Issue token</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="grid grid-cols-3 gap-3">
              <div className="col-span-2 space-y-1.5">
                <Label htmlFor="name">Name</Label>
                <Input
                  id="name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="my laptop"
                  required
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="days">Expires (days)</Label>
                <Input
                  id="days"
                  type="number"
                  min={1}
                  max={365}
                  value={days}
                  onChange={(e) => setDays(parseInt(e.target.value, 10) || 30)}
                  className="font-mono"
                />
              </div>
            </div>
            <div className="flex justify-end">
              <Button type="submit" disabled={issue.isPending}>
                <Plus className="h-4 w-4" />
                Issue
              </Button>
            </div>
          </CardContent>
        </Card>
      </form>

      {issued && (
        <Card className="border-[var(--accent)]/40">
          <CardHeader>
            <CardTitle>Token issued</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            <p className="text-xs text-[var(--text-secondary)]">
              Save this now — kuso never shows it again.
            </p>
            <div className="flex items-center gap-2">
              <code className="flex-1 truncate rounded border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1 font-mono text-xs">
                {issued.token}
              </code>
              <Button
                variant="outline"
                size="sm"
                type="button"
                onClick={() => copy(issued.token)}
              >
                <Copy className="h-3 w-3" />
                Copy
              </Button>
            </div>
            <Button variant="ghost" size="sm" type="button" onClick={() => setIssued(null)}>
              dismiss
            </Button>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Existing tokens</CardTitle>
        </CardHeader>
        <CardContent>
          {tokens.isPending && <p className="text-sm text-[var(--text-tertiary)]">loading…</p>}
          {tokens.data?.length === 0 && (
            <p className="text-sm text-[var(--text-tertiary)]">No tokens.</p>
          )}
          <ul className="divide-y divide-[var(--border-subtle)]">
            {tokens.data?.map((t) => (
              <li key={t.id} className="flex items-center justify-between py-2">
                <div className="min-w-0">
                  <p className="text-sm font-medium">{t.name}</p>
                  <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
                    issued {relativeTime(t.createdAt)} · expires{" "}
                    {new Date(t.expiresAt).toLocaleDateString()}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  type="button"
                  aria-label="Revoke"
                  onClick={() => revoke.mutate(t.id)}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>
    </div>
  );
}
