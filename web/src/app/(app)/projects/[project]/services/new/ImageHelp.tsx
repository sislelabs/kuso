"use client";

import { useState } from "react";
import { KeyRound, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useRegistryLogin, type RegistryCredential } from "@/features/registry-credentials";
import { describeError } from "@/lib/errors";

export interface RegistryLoginDraft {
  registry: string;
  username: string;
  password: string;
}

export function registryLoginComplete(d: RegistryLoginDraft): boolean {
  return !!d.registry.trim() && !!d.username.trim() && !!d.password;
}

// RegistryLoginFields is the inline private-registry login: registry,
// username and a token. The registry pre-fills from the image.
export function RegistryLoginFields({
  value,
  onChange,
  idPrefix,
}: {
  value: RegistryLoginDraft;
  onChange: (next: RegistryLoginDraft) => void;
  idPrefix: string;
}) {
  const set = (k: keyof RegistryLoginDraft) => (e: React.ChangeEvent<HTMLInputElement>) =>
    onChange({ ...value, [k]: e.target.value });
  const cls = "h-9 font-mono text-[13px]";
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
      <label className="space-y-1 text-xs text-[var(--text-secondary)]" htmlFor={`${idPrefix}-registry`}>
        <span className="block font-medium text-[var(--text-primary)]">Registry</span>
        <Input id={`${idPrefix}-registry`} value={value.registry} onChange={set("registry")} placeholder="e.g. ghcr.io" spellCheck={false} autoComplete="off" className={cls} />
      </label>
      <label className="space-y-1 text-xs text-[var(--text-secondary)]" htmlFor={`${idPrefix}-username`}>
        <span className="block font-medium text-[var(--text-primary)]">Username</span>
        <Input id={`${idPrefix}-username`} value={value.username} onChange={set("username")} spellCheck={false} autoComplete="off" className={cls} />
      </label>
      <label className="space-y-1 text-xs text-[var(--text-secondary)]" htmlFor={`${idPrefix}-password`}>
        <span className="block font-medium text-[var(--text-primary)]">Password or token</span>
        <Input
          id={`${idPrefix}-password`}
          type="password"
          value={value.password}
          onChange={set("password")}
          autoComplete="off"
          data-1p-ignore
          data-lpignore="true"
          className={cls}
        />
      </label>
    </div>
  );
}

// RegistryLoginInline saves a login on an existing project without
// leaving the add-service form, then hands back its secret name.
export function RegistryLoginInline({
  project,
  registry,
  onSaved,
  onCancel,
}: {
  project: string;
  registry: string;
  onSaved: (cred: RegistryCredential) => void;
  onCancel: () => void;
}) {
  const [draft, setDraft] = useState<RegistryLoginDraft>({ registry, username: "", password: "" });
  const [error, setError] = useState<string | null>(null);
  const login = useRegistryLogin(project);
  const save = async () => {
    if (!registryLoginComplete(draft)) {
      setError("Fill in the registry, username and password or token.");
      return;
    }
    try {
      const cred = await login.mutateAsync({
        registry: draft.registry.trim(),
        username: draft.username.trim(),
        password: draft.password,
      });
      setError(null);
      onSaved(cred);
    } catch (e) {
      setError(describeError(e, "Couldn't save the registry login"));
    }
  };
  return (
    <div className="space-y-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]/50 p-3">
      <p className="flex items-center gap-1.5 text-xs text-[var(--text-secondary)]">
        <KeyRound aria-hidden className="h-3.5 w-3.5" />
        Saved for the whole project and used for every pull from this registry. The token is never shown again.
      </p>
      <RegistryLoginFields value={draft} onChange={setDraft} idPrefix="inline-registry" />
      {error && (
        <p role="alert" className="text-xs text-[var(--error)]">
          {error}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onCancel} disabled={login.isPending}>
          Cancel
        </Button>
        <Button type="button" size="sm" onClick={() => void save()} disabled={login.isPending}>
          {login.isPending ? "Saving…" : "Save login"}
        </Button>
      </div>
    </div>
  );
}

// Images known to start under kuso's restrictions (non-root, no added
// capabilities), with the port each listens on.
export const QUICK_START_IMAGES = [
  { image: "nginxinc/nginx-unprivileged:1.27-alpine", port: "8080", label: "nginx (unprivileged)" },
  { image: "traefik/whoami:v1.11.0", port: "80", label: "whoami" },
] as const;

// RuntimeConstraints says up front what kuso's hardened runtime refuses,
// so a stock image that needs root fails here, not in a crash loop.
export function RuntimeConstraints({ onPick }: { onPick?: (image: string, port: string) => void }) {
  return (
    <div className="space-y-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]/50 px-3 py-2.5 text-xs leading-relaxed text-[var(--text-secondary)]">
      <p className="flex items-start gap-1.5">
        <ShieldCheck aria-hidden className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
        <span>
          Containers run with every Linux capability dropped. Images that need root powers at startup, like stock{" "}
          <span className="font-mono">nginx</span> or <span className="font-mono">httpd</span> (they{" "}
          <span className="font-mono">chown</span> files and switch user), crash. Use an unprivileged variant, or
          allow the capability later under Settings → Security.
        </span>
      </p>
      {onPick && (
        <p className="flex flex-wrap items-center gap-1.5 pl-5">
          <span>Known to work:</span>
          {QUICK_START_IMAGES.map((q) => (
            <button
              key={q.image}
              type="button"
              onClick={() => onPick(q.image, q.port)}
              title={`${q.image}, listens on ${q.port}`}
              className="rounded-full border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-0.5 font-mono text-[var(--text-primary)] hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]"
            >
              {q.label}
            </button>
          ))}
        </p>
      )}
    </div>
  );
}
