"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { useCanOnProject, Perms } from "@/features/auth";
import { getDeployHook, enableDeployHook, disableDeployHook, type DeployHook } from "@/features/github";
import { Field } from "./_primitives";

// DeployHookRow manages the service's deploy hook: a secret URL that
// starts a build. It is how pushes deploy when no GitHub App installation
// covers the repo — paste it into the repo's webhook settings, or POST to
// it from CI. The URL is the credential, so it is only fetched for users
// who can write the service.
export function DeployHookRow({ project, service }: { project: string; service: string }) {
  const qc = useQueryClient();
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const key = ["deploy-hook", project, service];
  const hook = useQuery({
    queryKey: key,
    queryFn: () => getDeployHook(project, service),
    enabled: canWrite,
    retry: false,
    meta: { skipGlobalErrorToast: true },
  });
  const [copied, setCopied] = useState(false);

  const onDone = (next: DeployHook) => qc.setQueryData(key, next);
  const enable = useMutation({
    mutationFn: (rotate: boolean) => enableDeployHook(project, service, rotate),
    onSuccess: (next, rotate) => {
      onDone(next);
      if (rotate) toast.success("Deploy hook rotated. The old URL no longer works.");
    },
  });
  const disable = useMutation({
    mutationFn: () => disableDeployHook(project, service),
    onSuccess: onDone,
  });
  const busy = enable.isPending || disable.isPending;

  if (!canWrite) return null;
  const url = hook.data?.url ?? "";

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Couldn't copy. Select the URL and copy it by hand.");
    }
  };

  return (
    <Field
      label="Deploy hook"
      hint={
        hook.data?.enabled
          ? "Add this URL as a repository webhook (JSON, push events) or POST to it from CI. Anyone with the URL can start a build."
          : "A secret URL that starts a build. Use it when the GitHub App doesn't cover this repository."
      }
    >
      {hook.data?.enabled ? (
        <div className="space-y-2">
          <div className="flex items-center gap-1.5">
            <input
              readOnly
              value={url}
              aria-label="Deploy hook URL"
              onFocus={(e) => e.currentTarget.select()}
              className="h-8 min-w-0 flex-1 rounded-sm border border-border bg-input px-3 font-mono text-[12px] text-[var(--text-secondary)] outline-none focus-visible:border-ring"
            />
            <button
              type="button"
              onClick={() => void copy()}
              aria-label="Copy deploy hook URL"
              title="Copy"
              className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-sm border border-border text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
            >
              {copied ? <Check className="h-3.5 w-3.5 text-[var(--success)]" /> : <Copy className="h-3.5 w-3.5" />}
            </button>
          </div>
          <div className="flex items-center gap-2">
            <Button type="button" size="xs" variant="outline" disabled={busy} onClick={() => enable.mutate(true)}>
              Rotate URL
            </Button>
            <Button type="button" size="xs" variant="ghost" disabled={busy} onClick={() => disable.mutate()}>
              Disable
            </Button>
          </div>
        </div>
      ) : (
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={busy || hook.isLoading}
          onClick={() => enable.mutate(false)}
        >
          {enable.isPending ? "Enabling…" : "Enable deploy hook"}
        </Button>
      )}
    </Field>
  );
}
