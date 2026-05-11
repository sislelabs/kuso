"use client";

import { useMemo, useState } from "react";
import { Button, buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Copy, ExternalLink, Github } from "lucide-react";
import { setServiceEnv, getServiceEnv } from "@/features/services/api";
import type { KusoEnvVar } from "@/types/projects";
import { toast } from "sonner";

// "Sign in with GitHub" provisioning helper for a deployed service.
//
// GitHub's OAuth-App callback validation is per-host (subdomain not
// equal to parent), so one OAuth App can't reasonably cover every
// kuso-deployed service. The supported flow is: register one OAuth App
// per service, store the Client ID + Secret as env vars on the
// service, and let Better Auth (or whatever the runtime uses) pick
// them up via the standard `<PREFIX>_CLIENT_ID` / `<PREFIX>_CLIENT_SECRET`
// convention.
//
// This dialog prefills the GitHub form via query-string parameters
// that the github.com/settings/applications/new page reads:
//   oauth_application[name]         → Application name
//   oauth_application[url]          → Homepage URL
//   oauth_application[callback_url] → Authorization callback URL
//   oauth_application[description]  → Description
//
// The user clicks "Open GitHub", registers the App, copies the Client
// ID + Secret back into the dialog, and clicks Save — we splice the
// values into the service's env list (read-modify-write so other
// vars survive).

const DEFAULT_CALLBACK_PATH = "/api/auth/callback/github";

interface AddOauthAppDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  project: string;
  service: string;
  // Public host the service serves on. Used to compute homepage +
  // callback URLs we tell GitHub about. e.g. "web.papelito.sislelabs.com".
  serviceHost: string;
}

export function AddOauthAppDialog({
  open,
  onOpenChange,
  project,
  service,
  serviceHost,
}: AddOauthAppDialogProps) {
  const [prefix, setPrefix] = useState("GITHUB");
  const [appName, setAppName] = useState(`${project}-${service}`);
  const [clientId, setClientId] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const [saving, setSaving] = useState(false);

  const homepage = `https://${serviceHost}`;
  const callback = `https://${serviceHost}${DEFAULT_CALLBACK_PATH}`;

  // Prefill URL — the page reads these query params at load and
  // populates the inputs. Description is short and identifying.
  const githubUrl = useMemo(() => {
    const u = new URL("https://github.com/settings/applications/new");
    u.searchParams.set("oauth_application[name]", appName);
    u.searchParams.set("oauth_application[url]", homepage);
    u.searchParams.set("oauth_application[callback_url]", callback);
    u.searchParams.set(
      "oauth_application[description]",
      `Sign-in for ${project}/${service} on kuso`,
    );
    return u.toString();
  }, [appName, homepage, callback, project, service]);

  async function copy(value: string, label: string) {
    try {
      await navigator.clipboard.writeText(value);
      toast.success(`${label} copied`);
    } catch {
      toast.error(`Couldn't copy ${label}`);
    }
  }

  async function onSave() {
    if (!clientId.trim() || !clientSecret.trim()) {
      toast.error("Paste both the Client ID and Client Secret");
      return;
    }
    const cleanPrefix = prefix.trim().toUpperCase() || "GITHUB";
    const idKey = `${cleanPrefix}_CLIENT_ID`;
    const secretKey = `${cleanPrefix}_CLIENT_SECRET`;

    setSaving(true);
    try {
      // Read-modify-write the env list. /env is complete-replace, so
      // forgetting to merge would wipe DATABASE_URL etc. We skip any
      // entry without a name (shouldn't happen, but KusoEnvVar.name
      // is typed optional).
      const { envVars } = await getServiceEnv(project, service);
      const byName = new Map<string, KusoEnvVar>();
      for (const e of envVars) {
        if (e.name) byName.set(e.name, e);
      }
      byName.set(idKey, { name: idKey, value: clientId.trim() });
      byName.set(secretKey, { name: secretKey, value: clientSecret.trim() });
      await setServiceEnv(project, service, Array.from(byName.values()));
      toast.success(`Saved ${idKey} + ${secretKey}. Redeploy to pick up the env.`);
      onOpenChange(false);
      setClientId("");
      setClientSecret("");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Github className="size-4" />
            Add &ldquo;Sign in with GitHub&rdquo;
          </DialogTitle>
          <DialogDescription>
            Register a GitHub OAuth App for{" "}
            <span className="font-mono">{project}/{service}</span> and store
            its Client ID + Secret as env vars on this service.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {/* Editable App name + env prefix. Defaults are usually fine. */}
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1">
              <Label htmlFor="app-name" className="text-xs">
                Application name
              </Label>
              <Input
                id="app-name"
                value={appName}
                onChange={(e) => setAppName(e.target.value)}
                className="font-mono text-xs"
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="env-prefix" className="text-xs">
                Env-var prefix
              </Label>
              <Input
                id="env-prefix"
                value={prefix}
                onChange={(e) => setPrefix(e.target.value)}
                className="font-mono text-xs"
                placeholder="GITHUB"
              />
              <p className="text-[10px] text-[var(--text-tertiary)]">
                Saved as {prefix.toUpperCase() || "GITHUB"}_CLIENT_ID /
                _SECRET.
              </p>
            </div>
          </div>

          {/* The fixed callback + homepage values we'll pre-fill on
              GitHub. Click-to-copy so the user can double-check the
              GitHub form matches. */}
          <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-3 text-xs">
            <div className="font-semibold text-[var(--text-secondary)] mb-1">
              Homepage URL
            </div>
            <CopyRow value={homepage} onCopy={() => copy(homepage, "Homepage URL")} />
            <div className="font-semibold text-[var(--text-secondary)] mt-2 mb-1">
              Authorization callback URL
            </div>
            <CopyRow value={callback} onCopy={() => copy(callback, "Callback URL")} />
          </div>

          {/* Open GitHub. Real <a> so the OS opens the user's default
              browser/profile — window.open can be popup-blocked. */}
          <a
            href={githubUrl}
            target="_blank"
            rel="noreferrer"
            className={`${buttonVariants({ variant: "outline" })} w-full`}
          >
            <ExternalLink className="size-3.5" />
            Open GitHub to register the App
          </a>

          {/* After GitHub spits out creds, paste here. */}
          <div className="space-y-2">
            <Label htmlFor="client-id" className="text-xs">
              Client ID
            </Label>
            <Input
              id="client-id"
              value={clientId}
              onChange={(e) => setClientId(e.target.value)}
              placeholder="Iv23li…"
              className="font-mono text-xs"
              autoComplete="off"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="client-secret" className="text-xs">
              Client Secret
            </Label>
            <Input
              id="client-secret"
              value={clientSecret}
              onChange={(e) => setClientSecret(e.target.value)}
              placeholder="github-generated, click 'Generate a new client secret'"
              className="font-mono text-xs"
              type="password"
              autoComplete="off"
            />
            <p className="text-[10px] text-[var(--text-tertiary)]">
              GitHub only shows the Client Secret once. If you lose it,
              return to the App page and click &ldquo;Generate a new
              client secret&rdquo;.
            </p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={onSave} disabled={saving || !clientId || !clientSecret}>
            {saving ? "Saving…" : "Save to env"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function CopyRow({ value, onCopy }: { value: string; onCopy: () => void }) {
  return (
    <div className="flex items-center gap-2">
      <code className="flex-1 truncate font-mono text-[11px] text-[var(--text-primary)]">
        {value}
      </code>
      <button
        type="button"
        onClick={onCopy}
        className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
        title="Copy"
        aria-label="Copy"
      >
        <Copy className="size-3" />
      </button>
    </div>
  );
}
