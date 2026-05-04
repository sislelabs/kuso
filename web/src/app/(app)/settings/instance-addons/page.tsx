"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import { api } from "@/lib/api-client";
import { Database, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

interface InstanceAddon {
  name: string;
  host: string;
  port?: string;
  user?: string;
  kind: string;
}

// /settings/instance-addons — admin-only registry of shared
// database servers that any project can carve a per-project DB
// out of (Model 2). Built on top of instance secrets but surfaces
// each registration as a connection card with the host/port parsed
// out of the DSN. Passwords never round-trip back to the browser.
//
// Wiring: registering "pg" with a DSN stores it as
// INSTANCE_ADDON_PG_DSN_ADMIN under the kuso-instance-shared kube
// Secret. Projects opt in via the addon canvas dialog → Instance
// tab → enter the addon name.
export default function InstanceAddonsPage() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const qc = useQueryClient();
  const list = useQuery<{ addons: InstanceAddon[] }>({
    queryKey: ["instance-addons"],
    queryFn: () => api("/api/instance-addons"),
    enabled: isAdmin,
  });
  const register = useMutation({
    mutationFn: ({ name, dsn }: { name: string; dsn: string }) =>
      api("/api/instance-addons", { method: "PUT", body: { name, dsn } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["instance-addons"] }),
    onError: (e) => toast.error(e instanceof Error ? e.message : "Register failed"),
  });
  const unregister = useMutation({
    mutationFn: (name: string) =>
      api(`/api/instance-addons/${encodeURIComponent(name)}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["instance-addons"] }),
    onError: (e) => toast.error(e instanceof Error ? e.message : "Unregister failed"),
  });

  const [showAdd, setShowAdd] = useState(false);
  const [newName, setNewName] = useState("");
  const [newDSN, setNewDSN] = useState("");

  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-3xl p-6 lg:p-8">
        <p className="rounded-md border border-amber-500/30 bg-amber-500/5 p-4 text-sm text-amber-400">
          Instance-wide addons are admin-only.
        </p>
      </div>
    );
  }

  const onSubmit = () => {
    const name = newName.trim().toLowerCase();
    const dsn = newDSN.trim();
    if (!name || !dsn) return;
    if (!/^[a-z][a-z0-9-]{0,30}[a-z0-9]?$/.test(name)) {
      toast.error("Name: lowercase, dashes, ≤32 chars, must start with a letter");
      return;
    }
    if (!/^postgres(ql)?:\/\//.test(dsn)) {
      toast.error("DSN must start with postgres:// or postgresql://");
      return;
    }
    register.mutate(
      { name, dsn },
      {
        onSuccess: () => {
          toast.success(`${name} registered — projects can now connect-instance to it`);
          setNewName("");
          setNewDSN("");
          setShowAdd(false);
        },
      }
    );
  };

  const addons = (list.data?.addons ?? []).slice().sort((a, b) => a.name.localeCompare(b.name));

  return (
    <div className="mx-auto max-w-3xl p-6 lg:p-8">
      <header className="mb-6 flex items-start gap-3">
        <Database className="mt-1 h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">Instance addons</h1>
          <p className="mt-1 text-[12px] leading-relaxed text-[var(--text-secondary)]">
            Shared database servers that projects can carve per-project DBs out of (Model 2). Register
            a superuser DSN here once; projects then opt in via{" "}
            <code className="rounded bg-[var(--bg-secondary)] px-1 font-mono text-[11px]">
              + addon → Instance
            </code>{" "}
            on the canvas, and kuso provisions an isolated{" "}
            <code className="rounded bg-[var(--bg-secondary)] px-1 font-mono text-[11px]">
              CREATE DATABASE
            </code>{" "}
            + role per project. Postgres only in v0.7.6.
          </p>
        </div>
      </header>

      {/* List */}
      <section className="space-y-3">
        <header className="flex items-baseline justify-between">
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            registered ({addons.length})
          </h2>
          {!showAdd && (
            <Button size="sm" variant="ghost" onClick={() => setShowAdd(true)}>
              <Plus className="h-3.5 w-3.5" />
              Register
            </Button>
          )}
        </header>
        {list.isPending ? (
          <Skeleton className="h-20 w-full" />
        ) : addons.length === 0 ? (
          <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-8 text-center text-[12px] text-[var(--text-tertiary)]">
            No instance addons yet. Register a shared Postgres above to let projects opt in.
          </p>
        ) : (
          <ul className="space-y-2">
            {addons.map((a) => (
              <li
                key={a.name}
                className="flex items-center gap-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-2"
              >
                <Database className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline gap-2">
                    <span className="font-mono text-[13px] font-medium text-[var(--text-primary)]">
                      {a.name}
                    </span>
                    <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                      {a.kind}
                    </span>
                  </div>
                  <p className="mt-0.5 truncate font-mono text-[10px] text-[var(--text-tertiary)]">
                    {a.user ? `${a.user}@` : ""}
                    {a.host || "<unparseable host>"}
                    {a.port ? `:${a.port}` : ""}
                  </p>
                </div>
                <button
                  type="button"
                  onClick={() => {
                    if (
                      !confirm(
                        `Unregister instance addon "${a.name}"?\n\nProjects that opted in will keep their per-project DBs and connection secrets, but no new projects can opt in until you re-register.`
                      )
                    ) {
                      return;
                    }
                    unregister.mutate(a.name);
                  }}
                  disabled={unregister.isPending}
                  className="rounded p-1 text-[var(--text-tertiary)] hover:bg-red-500/10 hover:text-red-400 disabled:opacity-40"
                  aria-label={`Unregister ${a.name}`}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* Register form */}
      {showAdd && (
        <section className="mt-6 space-y-3">
          <header>
            <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              register
            </h2>
          </header>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              onSubmit();
            }}
            className="flex flex-col gap-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-3"
          >
            <div>
              <label
                htmlFor="instance-addon-name"
                className="mb-1 block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
              >
                name
              </label>
              <Input
                id="instance-addon-name"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                placeholder="pg"
                className="h-8 font-mono text-[12px]"
                spellCheck={false}
                autoComplete="off"
              />
              <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
                Lowercase, dashes, ≤32 chars. Projects reference this name when opting in.
              </p>
            </div>
            <div>
              <label
                htmlFor="instance-addon-dsn"
                className="mb-1 block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
              >
                superuser dsn
              </label>
              <Input
                id="instance-addon-dsn"
                value={newDSN}
                onChange={(e) => setNewDSN(e.target.value)}
                type="password"
                placeholder="postgres://admin:pw@shared-pg.example.com:5432/postgres?sslmode=disable"
                className="h-8 font-mono text-[12px]"
                spellCheck={false}
                autoComplete="off"
              />
              <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
                Needs CREATE DATABASE + CREATE ROLE privileges. Stored as{" "}
                <code>INSTANCE_ADDON_{newName.toUpperCase() || "&lt;NAME&gt;"}_DSN_ADMIN</code>.
              </p>
            </div>
            <div className="flex items-center justify-end gap-2">
              <Button
                size="sm"
                variant="ghost"
                type="button"
                onClick={() => {
                  setShowAdd(false);
                  setNewName("");
                  setNewDSN("");
                }}
                disabled={register.isPending}
              >
                Cancel
              </Button>
              <Button size="sm" type="submit" disabled={!newName.trim() || !newDSN.trim() || register.isPending}>
                <Plus className="h-3.5 w-3.5" />
                {register.isPending ? "Registering…" : "Register"}
              </Button>
            </div>
          </form>
        </section>
      )}
    </div>
  );
}
