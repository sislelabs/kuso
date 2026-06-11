"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useConfigureGithub,
  useSetupStatus,
  useInstallations,
} from "@/features/github";
import type { ConfigureBody, GithubInstallation } from "@/features/github";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { ErrorBoundary } from "@/components/shared/ErrorBoundary";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import {
  Github,
  RotateCw,
  ExternalLink,
  ShieldCheck,
  ChevronDown,
  ChevronRight,
  Copy,
  Check,
  RefreshCw,
  Plus,
  Lock,
  Globe,
  Building2,
  User as UserIcon,
} from "lucide-react";

// /settings/github — paste GitHub App credentials so kuso can monitor
// repos and trigger builds. The wizard targets the case of an admin who
// installed kuso WITHOUT --github-wizard and now wants to connect
// GitHub without ssh-ing back to the box. The server handler:
//   1. validates the inputs (PEM parse, numeric App ID),
//   2. writes the kuso-github-app Secret in the kuso namespace,
//   3. patches the kuso-server Deployment template metadata so the pod
//      restarts and re-reads env on boot.
// Step 3 is why we show a "restarting" state and poll /healthz until
// the pod is back — hot-reloading github.Config in-process is too
// fragile (cached App JWT signers, dispatcher singletons).
export default function GithubSettingsPageWithBoundary() {
  return (
    <ErrorBoundary
      fallback={
        <div className="mx-auto max-w-3xl p-6 lg:p-8">
          <div className="rounded-md border border-red-500/30 bg-red-500/5 p-4 text-sm">
            <p className="font-medium text-[var(--text-primary)]">
              Something broke on the GitHub setup page
            </p>
            <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
              An unexpected error happened while rendering. Try reloading.
            </p>
            <div className="mt-3">
              <Button size="sm" variant="outline" onClick={() => window.location.reload()}>
                Reload
              </Button>
            </div>
          </div>
        </div>
      }
    >
      <GithubSettingsPage />
    </ErrorBoundary>
  );
}

function GithubSettingsPage() {
  const qc = useQueryClient();
  const status = useSetupStatus();
  const configure = useConfigureGithub();
  const [restartPolling, setRestartPolling] = useState(false);

  // Paste fields. Match the keys server-go expects in
  // configureRequest. Defaults all blank — pre-filling something here
  // would be a security smell.
  const [appId, setAppId] = useState("");
  const [appSlug, setAppSlug] = useState("");
  const [clientId, setClientId] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const [webhookSecret, setWebhookSecret] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [org, setOrg] = useState("");
  const [showReconfigure, setShowReconfigure] = useState(false);

  const isConfigured = !!status.data?.configured && !showReconfigure;

  // After a successful configure we poll /healthz to know when the
  // restart is done. /healthz returns 200 with a {version} body, but
  // during the rollout the pod is briefly unreachable and the request
  // fails with a network error or a 502 from traefik. We retry for up
  // to ~60s.
  useEffect(() => {
    if (!restartPolling) return;
    const start = Date.now();
    const id = setInterval(async () => {
      try {
        const res = await fetch("/healthz", { cache: "no-store" });
        if (res.ok) {
          clearInterval(id);
          setRestartPolling(false);
          await Promise.all([
            qc.invalidateQueries({ queryKey: ["github", "setup-status"] }),
            qc.invalidateQueries({ queryKey: ["github", "install-url"] }),
            qc.invalidateQueries({ queryKey: ["github", "installations"] }),
          ]);
          toast.success("kuso-server is back online");
          setShowReconfigure(false);
        }
      } catch {
        /* still rolling */
      }
      if (Date.now() - start > 90_000) {
        clearInterval(id);
        setRestartPolling(false);
        toast.error("Server didn't come back within 90s — reload to check");
      }
    }, 3_000);
    return () => clearInterval(id);
  }, [restartPolling, qc]);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const body: ConfigureBody = {
      appId: appId.trim(),
      appSlug: appSlug.trim(),
      clientId: clientId.trim(),
      clientSecret: clientSecret.trim(),
      webhookSecret: webhookSecret.trim(),
      privateKey,
      org: org.trim() || undefined,
    };
    try {
      await configure.mutateAsync(body);
      toast.success("Saved — waiting for kuso-server to restart");
      setRestartPolling(true);
      // Clear the form so a refresh after restart shows a clean state.
      setAppId("");
      setAppSlug("");
      setClientId("");
      setClientSecret("");
      setWebhookSecret("");
      setPrivateKey("");
      setOrg("");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save GitHub App");
    }
  };

  if (status.isPending) {
    return (
      <div className="mx-auto max-w-3xl p-6 text-sm text-[var(--text-tertiary)] lg:p-8">
        Loading…
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-3xl p-6 lg:p-8">
      <header className="mb-6 flex items-center gap-3">
        <Github className="h-5 w-5 text-[var(--text-secondary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">GitHub App</h1>
          <p className="text-sm text-[var(--text-secondary)]">
            Connect a GitHub App so kuso can list repos, trigger builds on push, and post
            preview URLs on PRs.
          </p>
        </div>
      </header>

      {restartPolling ? (
        <RestartingPanel />
      ) : isConfigured ? (
        <ConfiguredPanel
          appId={status.data?.appId}
          appSlug={status.data?.appSlug}
          onReconfigure={() => setShowReconfigure(true)}
        />
      ) : (
        <WizardForm
          appId={appId}
          setAppId={setAppId}
          appSlug={appSlug}
          setAppSlug={setAppSlug}
          clientId={clientId}
          setClientId={setClientId}
          clientSecret={clientSecret}
          setClientSecret={setClientSecret}
          webhookSecret={webhookSecret}
          setWebhookSecret={setWebhookSecret}
          privateKey={privateKey}
          setPrivateKey={setPrivateKey}
          org={org}
          setOrg={setOrg}
          submitting={configure.isPending}
          onSubmit={onSubmit}
          onCancel={status.data?.configured ? () => setShowReconfigure(false) : undefined}
        />
      )}
    </div>
  );
}

function ConfiguredPanel({
  appId,
  appSlug,
  onReconfigure,
}: {
  appId?: string;
  appSlug?: string;
  onReconfigure: () => void;
}) {
  const qc = useQueryClient();
  const installs = useInstallations();
  const [openInstall, setOpenInstall] = useState<number | null>(null);
  const webhookHealth = useQuery({
    queryKey: ["admin", "github", "webhook-health"],
    queryFn: () =>
      api<{ configured: boolean; lastDeliveryAt?: string; lastDeliveryEvent?: string }>(
        "/api/github/webhook-health"
      ),
    refetchInterval: 30_000,
  });
  const refresh = useMutation({
    mutationFn: () => api("/api/github/installations/refresh", { method: "POST" }),
    onSuccess: () => {
      toast.success("Installations refreshed");
      qc.invalidateQueries({ queryKey: ["github", "installations"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Refresh failed"),
  });

  const installURL = appSlug ? `https://github.com/apps/${appSlug}/installations/new` : null;
  const adminURL = appSlug ? `https://github.com/settings/apps/${appSlug}` : null;
  const installations = installs.data ?? [];
  const totalRepos = installations.reduce((sum, i) => sum + (i.repositories?.length ?? 0), 0);
  const totalPrivate = installations.reduce(
    (sum, i) => sum + (i.repositories ?? []).filter((r) => r.private).length,
    0,
  );

  return (
    <section className="space-y-4">
      {/* Top status banner — keep the green-checkmark frame from before
          but pack the identity row tighter and put the action chips
          inline with it so the eye lands on "what is this" + "what
          can I do" without scrolling. */}
      <div className="rounded-md border border-emerald-500/30 bg-emerald-500/5 p-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex items-start gap-3">
            <ShieldCheck className="mt-0.5 h-4 w-4 flex-shrink-0 text-emerald-500" />
            <div className="flex-1">
              <p className="text-sm font-medium text-[var(--text-primary)]">
                GitHub App is configured
              </p>
              <dl className="mt-2 grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1 font-mono text-[12px] text-[var(--text-secondary)]">
                {appSlug && (
                  <>
                    <dt className="text-[var(--text-tertiary)]">slug</dt>
                    <dd className="flex items-center gap-1.5">
                      <a
                        href={`https://github.com/apps/${appSlug}`}
                        target="_blank"
                        rel="noreferrer"
                        className="text-[var(--accent)] hover:underline"
                      >
                        {appSlug}
                      </a>
                      <CopyChip value={appSlug} />
                    </dd>
                  </>
                )}
                {appId && (
                  <>
                    <dt className="text-[var(--text-tertiary)]">id</dt>
                    <dd className="flex items-center gap-1.5 text-[var(--text-primary)]">
                      {appId}
                      <CopyChip value={appId} />
                    </dd>
                  </>
                )}
                <dt className="text-[var(--text-tertiary)]">webhook</dt>
                <dd className="flex items-center gap-1.5 text-[var(--text-primary)]">
                  <span className="truncate">{webhookURL()}</span>
                  <CopyChip value={webhookURL()} />
                </dd>
              </dl>
            </div>
          </div>
          <div className="flex flex-wrap gap-2">
            {installURL && (
              <a
                href={installURL}
                target="_blank"
                rel="noreferrer"
                title="Opens GitHub's account picker — install kuso on any org or user account you administer"
                className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-3 text-xs font-medium hover:bg-[var(--accent-subtle)]"
              >
                <ExternalLink className="h-3.5 w-3.5" />
                Add org / repo
              </a>
            )}
            {adminURL && (
              <a
                href={adminURL}
                target="_blank"
                rel="noreferrer"
                className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-3 text-xs font-medium hover:bg-[var(--accent-subtle)]"
              >
                <ExternalLink className="h-3.5 w-3.5" />
                Manage on GitHub
              </a>
            )}
            <Button size="sm" variant="outline" onClick={onReconfigure}>
              Reconfigure
            </Button>
          </div>
        </div>
      </div>

      {/* Installations / repo coverage. The previous page showed
          nothing here; users had no way to see which orgs/repos kuso
          actually has access to without leaving for github.com. The
          per-installation list mirrors the new-service repo picker
          so the source of truth is visible in settings too. */}
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <div className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-2.5">
          <div>
            <h2 className="text-sm font-semibold tracking-tight">Installations</h2>
            <p className="mt-0.5 text-[11px] text-[var(--text-tertiary)]">
              {installs.isPending
                ? "loading…"
                : installations.length === 0
                  ? "No orgs connected yet — click Add org to connect one."
                  : `${installations.length} ${installations.length === 1 ? "installation" : "installations"} · ${totalRepos} ${totalRepos === 1 ? "repo" : "repos"} accessible (${totalPrivate} private)`}
            </p>
          </div>
          <div className="flex items-center gap-2">
            {installURL && (
              <a
                href={installURL}
                target="_blank"
                rel="noreferrer"
                title="Install kuso on another org or user account, then Refresh"
                className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--accent)]/40 bg-[var(--accent-subtle)] px-3 text-xs font-medium text-[var(--accent)] hover:bg-[var(--accent-subtle)]/70"
              >
                <Plus className="h-3.5 w-3.5" />
                Add org
              </a>
            )}
            <Button
              size="sm"
              variant="outline"
              onClick={() => refresh.mutate()}
              disabled={refresh.isPending}
            >
              <RefreshCw className={cn("h-3.5 w-3.5", refresh.isPending && "animate-spin")} />
              Refresh
            </Button>
          </div>
        </div>
        {installations.length === 0 && !installs.isPending ? (
          <div className="px-4 py-6 text-center text-[12px] text-[var(--text-tertiary)]">
            kuso can&apos;t see any repos yet. Click{" "}
            <strong className="text-[var(--text-secondary)]">Add org</strong> to grant access to an
            organization or user account. Refresh after installing if the row doesn&apos;t appear
            within ~10s.
          </div>
        ) : (
          <ul className="divide-y divide-[var(--border-subtle)]">
            {installations.map((inst) => (
              <InstallationRow
                key={inst.id}
                inst={inst}
                expanded={openInstall === inst.id}
                onToggle={() =>
                  setOpenInstall((cur) => (cur === inst.id ? null : inst.id))
                }
              />
            ))}
          </ul>
        )}
      </section>

      {/* Webhook health — surfaced in-product from the last verified
          delivery kuso stamped (Setting github.lastDeliveryAt), so an
          operator can confirm pushes/PRs are actually reaching kuso
          without leaving for GitHub. The deliveries link stays as the
          source of truth for per-delivery detail. */}
      <div className="space-y-1.5 text-[12px] text-[var(--text-secondary)]">
        {webhookHealth.data?.lastDeliveryAt ? (
          <p>
            <span className="text-[var(--text-tertiary)]">Last webhook received:</span>{" "}
            <span className="font-mono">
              {new Date(webhookHealth.data.lastDeliveryAt).toLocaleString()}
            </span>
            {webhookHealth.data.lastDeliveryEvent && (
              <span className="text-[var(--text-tertiary)]">
                {" "}({webhookHealth.data.lastDeliveryEvent})
              </span>
            )}
          </p>
        ) : (
          <p className="text-amber-300/90">
            No webhook delivery has reached kuso yet. Push to a connected repo (or check the
            deliveries tab below) — if pushes never arrive, kuso&apos;s public URL may be
            unreachable from GitHub or the webhook secret drifted.
          </p>
        )}
        {appSlug && (
          <p className="text-[var(--text-tertiary)]">
            Per-delivery detail (incl. failures) lives on the App&apos;s{" "}
            <a
              href={`https://github.com/settings/apps/${appSlug}/advanced`}
              target="_blank"
              rel="noreferrer"
              className="text-[var(--accent)] hover:underline"
            >
              Recent Deliveries
            </a>{" "}
            tab.
          </p>
        )}
      </div>
    </section>
  );
}

function InstallationRow({
  inst,
  expanded,
  onToggle,
}: {
  inst: GithubInstallation;
  expanded: boolean;
  onToggle: () => void;
}) {
  const repos = inst.repositories ?? [];
  const privateCount = repos.filter((r) => r.private).length;
  const isOrg = inst.accountType?.toLowerCase() === "organization";
  const ownerURL = `https://github.com/${inst.accountLogin}`;
  return (
    <li className="px-4 py-2.5">
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center justify-between gap-3 text-left hover:bg-[var(--bg-tertiary)]/30 -mx-4 px-4 py-1 rounded"
      >
        <div className="flex min-w-0 items-center gap-3">
          {isOrg ? (
            <Building2 className="h-4 w-4 flex-shrink-0 text-[var(--text-tertiary)]" />
          ) : (
            <UserIcon className="h-4 w-4 flex-shrink-0 text-[var(--text-tertiary)]" />
          )}
          <div className="min-w-0">
            <a
              href={ownerURL}
              target="_blank"
              rel="noreferrer"
              onClick={(e) => e.stopPropagation()}
              className="text-sm font-medium hover:underline"
            >
              {inst.accountLogin}
            </a>
            <span className="ml-2 text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              {inst.accountType}
            </span>
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
              installation #{inst.id} · {repos.length} {repos.length === 1 ? "repo" : "repos"}
              {privateCount > 0 ? ` · ${privateCount} private` : ""}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <a
            href={`https://github.com/settings/installations/${inst.id}`}
            target="_blank"
            rel="noreferrer"
            onClick={(e) => e.stopPropagation()}
            className="inline-flex h-7 items-center gap-1 rounded border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-2 text-[11px] hover:bg-[var(--accent-subtle)]"
          >
            <ExternalLink className="h-3 w-3" />
            Configure
          </a>
          {expanded ? (
            <ChevronDown className="h-4 w-4 text-[var(--text-tertiary)]" />
          ) : (
            <ChevronRight className="h-4 w-4 text-[var(--text-tertiary)]" />
          )}
        </div>
      </button>
      {expanded && (
        <ul className="mt-2 ml-7 space-y-1">
          {repos.length === 0 ? (
            <li className="text-[11px] text-[var(--text-tertiary)]">
              No repos selected. Use{" "}
              <strong>Configure</strong> on the right to grant access.
            </li>
          ) : (
            repos.map((r) => (
              <li
                key={r.id}
                className="flex items-center justify-between gap-2 rounded px-2 py-1 hover:bg-[var(--bg-tertiary)]/30"
              >
                <a
                  href={`https://github.com/${r.fullName}`}
                  target="_blank"
                  rel="noreferrer"
                  className="flex min-w-0 items-center gap-1.5 font-mono text-[11px] hover:underline"
                >
                  {r.private ? (
                    <Lock className="h-3 w-3 flex-shrink-0 text-[var(--text-tertiary)]" />
                  ) : (
                    <Globe className="h-3 w-3 flex-shrink-0 text-[var(--text-tertiary)]" />
                  )}
                  <span className="truncate">{r.fullName}</span>
                  <span className="text-[10px] text-[var(--text-tertiary)]">@{r.defaultBranch}</span>
                </a>
              </li>
            ))
          )}
        </ul>
      )}
    </li>
  );
}

function CopyChip({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1200);
        } catch {
          /* ignore */
        }
      }}
      className="inline-flex h-4 w-4 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
      aria-label="Copy"
    >
      {copied ? <Check className="h-2.5 w-2.5 text-emerald-400" /> : <Copy className="h-2.5 w-2.5" />}
    </button>
  );
}

function RestartingPanel() {
  return (
    <section className="rounded-md border border-amber-500/30 bg-amber-500/5 p-4">
      <div className="flex items-start gap-3">
        <RotateCw className="mt-0.5 h-4 w-4 flex-shrink-0 animate-spin text-amber-500" />
        <div className="flex-1">
          <p className="text-sm font-medium text-[var(--text-primary)]">
            kuso-server is restarting…
          </p>
          <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
            The new GitHub App credentials are saved. The kuso server pod is rolling so it
            picks up the new env. This page will refresh automatically when the server is
            back online (~30s typically).
          </p>
        </div>
      </div>
    </section>
  );
}

interface WizardFormProps {
  appId: string;
  setAppId: (v: string) => void;
  appSlug: string;
  setAppSlug: (v: string) => void;
  clientId: string;
  setClientId: (v: string) => void;
  clientSecret: string;
  setClientSecret: (v: string) => void;
  webhookSecret: string;
  setWebhookSecret: (v: string) => void;
  privateKey: string;
  setPrivateKey: (v: string) => void;
  org: string;
  setOrg: (v: string) => void;
  submitting: boolean;
  onSubmit: (e: React.FormEvent) => void;
  onCancel?: () => void;
}

function WizardForm(props: WizardFormProps) {
  return (
    <form onSubmit={props.onSubmit} className="space-y-4">
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <div className="border-b border-[var(--border-subtle)] px-4 py-2.5">
          <h2 className="text-sm font-semibold tracking-tight">Step 1 — Create a GitHub App</h2>
        </div>
        <div className="space-y-3 px-4 py-3 text-sm text-[var(--text-secondary)]">
          <ol className="list-decimal space-y-1 pl-5">
            <li>
              Open{" "}
              <a
                href="https://github.com/settings/apps/new"
                target="_blank"
                rel="noreferrer"
                className="underline hover:text-[var(--text-primary)]"
              >
                github.com/settings/apps/new
              </a>{" "}
              (or your org's <code className="font-mono text-[12px]">/organizations/&lt;org&gt;/settings/apps/new</code>).
            </li>
            <li>
              Set <strong>Webhook URL</strong> to{" "}
              <code className="font-mono text-[12px]">{webhookURL()}</code> and copy any random
              secret into <strong>Webhook secret</strong>. Save the same secret below.
            </li>
            <li>
              Set <strong>Setup URL</strong> to{" "}
              <code className="font-mono text-[12px]">{setupURL()}</code> (this is what makes
              the install redirect back into kuso).
            </li>
            <li>
              Permissions:{" "}
              <span className="font-mono text-[12px]">
                Contents: Read · Metadata: Read · Pull requests: Read &amp; Write · Webhooks: Read
                &amp; Write · Deployments: Read &amp; Write
              </span>
              . Subscribe to events: <span className="font-mono text-[12px]">push, pull_request, installation, installation_repositories</span>.
            </li>
            <li>After creating the App, generate + download a private key (.pem).</li>
          </ol>
        </div>
      </section>

      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <div className="border-b border-[var(--border-subtle)] px-4 py-2.5">
          <h2 className="text-sm font-semibold tracking-tight">Step 2 — Paste credentials</h2>
        </div>
        <div className="grid grid-cols-2 gap-x-3 gap-y-3 px-4 py-3">
          <Field label="App ID" htmlFor="appId" hint="numeric, top of the App settings page">
            <Input
              id="appId"
              value={props.appId}
              onChange={(e) => props.setAppId(e.target.value)}
              required
              placeholder="123456"
              className="h-8 text-[13px]"
            />
          </Field>
          <Field label="App slug" htmlFor="appSlug" hint="github.com/apps/<this>">
            <Input
              id="appSlug"
              value={props.appSlug}
              onChange={(e) => props.setAppSlug(e.target.value)}
              required
              placeholder="my-kuso"
              className="h-8 text-[13px]"
            />
          </Field>
          <Field label="Client ID" htmlFor="clientId" hint="App settings → Identifying & authorizing users">
            <Input
              id="clientId"
              value={props.clientId}
              onChange={(e) => props.setClientId(e.target.value)}
              required
              placeholder="Iv23li…"
              className="h-8 text-[13px]"
            />
          </Field>
          <Field label="Client secret" htmlFor="clientSecret" hint="generate one on the App settings page">
            <Input
              id="clientSecret"
              type="password"
              value={props.clientSecret}
              onChange={(e) => props.setClientSecret(e.target.value)}
              required
              placeholder="••••••••"
              className="h-8 text-[13px]"
            />
          </Field>
          <Field label="Webhook secret" htmlFor="webhookSecret" hint="same value you set on the App page" colSpan={2}>
            <Input
              id="webhookSecret"
              type="password"
              value={props.webhookSecret}
              onChange={(e) => props.setWebhookSecret(e.target.value)}
              required
              placeholder="••••••••"
              className="h-8 text-[13px]"
            />
          </Field>
          <Field label="Org (optional)" htmlFor="org" hint="informational, defaults to App owner" colSpan={2}>
            <Input
              id="org"
              value={props.org}
              onChange={(e) => props.setOrg(e.target.value)}
              placeholder="my-github-org"
              className="h-8 text-[13px]"
            />
          </Field>
          <Field
            label="Private key (.pem)"
            htmlFor="privateKey"
            hint="paste the entire file contents — multiline OK"
            colSpan={2}
          >
            <textarea
              id="privateKey"
              value={props.privateKey}
              onChange={(e) => props.setPrivateKey(e.target.value)}
              required
              placeholder={`-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA…\n-----END RSA PRIVATE KEY-----`}
              rows={8}
              className="block w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1.5 font-mono text-[11px] outline-none focus:border-[var(--accent)]"
            />
          </Field>
        </div>
      </section>

      <div className="flex justify-end gap-2">
        {props.onCancel && (
          <Button type="button" variant="outline" size="sm" onClick={props.onCancel}>
            Cancel
          </Button>
        )}
        <Button type="submit" size="sm" disabled={props.submitting}>
          {props.submitting ? "Saving…" : "Save and restart"}
        </Button>
      </div>
      <p className="text-[12px] text-[var(--text-tertiary)]">
        Saving writes the credentials into the <code>kuso-github-app</code> Kubernetes Secret
        and restarts <code>deploy/kuso-server</code> so the new env loads. Brief
        (~30s) downtime — the page polls until the server is back.
      </p>
    </form>
  );
}

function Field({
  label,
  htmlFor,
  colSpan,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  colSpan?: 2;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={colSpan === 2 ? "col-span-2 space-y-1" : "space-y-1"}>
      <label
        htmlFor={htmlFor}
        className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
      >
        {label}
      </label>
      {children}
      {hint && <p className="text-[11px] text-[var(--text-tertiary)]">{hint}</p>}
    </div>
  );
}

function webhookURL() {
  if (typeof window === "undefined") return "https://your-kuso/api/webhooks/github";
  return `${window.location.origin}/api/webhooks/github`;
}

function setupURL() {
  if (typeof window === "undefined") return "https://your-kuso/api/github/setup-callback";
  return `${window.location.origin}/api/github/setup-callback`;
}
