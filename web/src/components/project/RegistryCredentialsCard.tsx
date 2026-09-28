"use client";

import { useState } from "react";
import { Container, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import {
  useRegistryCredentials,
  useRegistryLogin,
  useRegistryLogout,
} from "@/features/registry-credentials";

// Private-registry logins for runtime=image services. Each credential is
// a per-project dockerconfigjson Secret; a service opts in from its
// Image settings. The password field is write-only.
export function RegistryCredentialsCard({ project }: { project: string }) {
  const list = useRegistryCredentials(project);
  const login = useRegistryLogin(project);
  const logout = useRegistryLogout(project);
  const [registry, setRegistry] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);

  const creds = list.data ?? [];
  const canSave = registry.trim() && username.trim() && password && !login.isPending;

  const onSave = () => {
    if (!canSave) return;
    login.mutate(
      { registry: registry.trim(), username: username.trim(), password },
      {
        onSuccess: (c) => {
          toast.success(`Saved ${c.registry} credential`);
          setRegistry("");
          setUsername("");
          setPassword("");
        },
        onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
      },
    );
  };

  return (
    <section className="space-y-4">
      <header>
        <h3 className="font-heading text-sm font-semibold tracking-tight">Registry credentials</h3>
        <p className="mt-1 text-[12px] leading-relaxed text-[var(--text-secondary)]">
          Logins for private registries (GHCR, ECR, Docker Hub). Attach one to a pre-built image
          service under its Image settings. Passwords are never shown again.
        </p>
      </header>

      {creds.length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-6 text-center text-[12px] text-[var(--text-tertiary)]">
          No registry credentials yet.
        </p>
      ) : (
        <ul className="overflow-hidden rounded-md border border-[var(--border-subtle)]">
          {creds.map((c) => (
            <li
              key={c.secretName}
              className="flex items-center gap-2 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-2 last:border-b-0"
            >
              <Container className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" />
              <span className="font-mono text-[12px] text-[var(--text-primary)]">{c.registry}</span>
              <span className="flex-1 truncate font-mono text-[11px] text-[var(--text-tertiary)]">
                {c.username}
              </span>
              <button
                type="button"
                onClick={() => setPendingDelete(c.registry)}
                disabled={logout.isPending}
                className="rounded p-1 text-[var(--text-tertiary)] hover:bg-red-500/10 hover:text-red-400 disabled:opacity-40"
                aria-label={`Remove ${c.registry} credential`}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </button>
            </li>
          ))}
        </ul>
      )}

      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSave();
        }}
        className="grid grid-cols-1 gap-2 sm:grid-cols-[1fr_1fr_1fr_auto]"
      >
        <Input
          value={registry}
          onChange={(e) => setRegistry(e.target.value)}
          placeholder="ghcr.io"
          aria-label="Registry host"
          className="h-8 font-mono text-[12px]"
          spellCheck={false}
        />
        <Input
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          placeholder="username"
          aria-label="Registry username"
          className="h-8 font-mono text-[12px]"
          spellCheck={false}
          autoComplete="off"
        />
        <Input
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          type="password"
          placeholder="password or token"
          aria-label="Registry password or token"
          className="h-8 font-mono text-[12px]"
          autoComplete="new-password"
        />
        <Button size="sm" type="submit" disabled={!canSave}>
          <Plus className="h-3.5 w-3.5" />
          {login.isPending ? "Saving…" : "Save"}
        </Button>
      </form>

      <ConfirmDialog
        open={pendingDelete !== null}
        title="Remove registry credential?"
        body={
          <p>
            The{" "}
            <span className="font-mono text-[var(--text-primary)]">{pendingDelete}</span>{" "}
            password is not recoverable. kuso refuses while a service still uses it.
          </p>
        }
        confirmLabel="Remove"
        pending={logout.isPending}
        onConfirm={() => {
          if (!pendingDelete) return;
          logout.mutate(pendingDelete, {
            onSuccess: () => {
              toast.success(`Removed ${pendingDelete} credential`);
              setPendingDelete(null);
            },
            onError: (e) => {
              toast.error(e instanceof Error ? e.message : "Remove failed");
              setPendingDelete(null);
            },
          });
        }}
        onCancel={() => setPendingDelete(null)}
      />
    </section>
  );
}
