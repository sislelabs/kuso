"use client";

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, Check, Clock, ExternalLink, Globe, Info, Lock, Plus, RefreshCw, Trash2, X, Zap } from "lucide-react";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { HostClashHint } from "@/components/shared/HostClashHint";
import { useIngressTargets } from "@/features/cluster";
import { useEnvDomainMutations } from "@/features/services";
import { api, ApiError } from "@/lib/api-client";
import { hostScheme } from "@/lib/host-scheme";
import { cn } from "@/lib/utils";
import type { KusoEnvironment } from "@/types/projects";
import { Section, Callout, Code, monoInputClass } from "./_primitives";
import {
  BUDGET_WARN_AT,
  diagnose,
  hostError,
  normalizeHostInput,
  recentAdds,
  registrableDomain,
  resolveCheck,
  suggestRecords,
  summarizeDomain,
  type DomainKind,
  type DomainStatus,
  type DomainStatusResponse,
  type DomainSummary,
  type ResolveCheck,
  type Tone,
} from "./domainHelpers";

// DomainsCard lists every hostname this service answers on in the viewed
// environment (the generated default plus custom and wildcard domains)
// with live DNS + TLS status, and adds/removes custom domains. Unlike the
// rest of the settings these apply immediately: each add/remove is its
// own API call, so there is nothing to Save.
export function DomainsCard({
  project,
  service,
  envName,
  env,
  envLoading,
  canWrite,
}: {
  project: string;
  service: string;
  envName: string;
  env?: KusoEnvironment;
  envLoading: boolean;
  canWrite: boolean;
}) {
  const status = useDomainStatus(project, service, envName, !!env);
  const qc = useQueryClient();
  const { add, remove } = useEnvDomainMutations(project, service, envName);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<{ host: string; kind: DomainKind } | null>(null);
  // Hosts added in this session stay expanded (instructions visible) until
  // the status endpoint reports them valid.
  const [justAdded, setJustAdded] = useState<string[]>([]);

  const rows = useMemo(() => {
    const out: { host: string; kind: DomainKind; tlsSecret?: string }[] = [];
    if (env?.spec.host) out.push({ host: env.spec.host, kind: "auto" });
    for (const h of env?.spec.additionalHosts ?? []) out.push({ host: h, kind: "custom" });
    for (const w of env?.spec.wildcardDomains ?? []) out.push({ host: w.host, kind: "wildcard", tlsSecret: w.tlsSecret });
    return out;
  }, [env]);
  const byHost = useMemo(() => {
    const m = new Map<string, DomainStatus>();
    for (const d of status.data?.domains ?? []) m.set(d.host.toLowerCase(), d);
    return m;
  }, [status.data]);
  // Older servers: 404, or 405 where the path matches the DELETE /domains/{host} route.
  const unsupported = status.error instanceof ApiError && (status.error.status === 404 || status.error.status === 405);
  const statusUnavailable = unsupported || (status.isError && !status.data);
  const customCount = rows.filter((r) => r.kind !== "auto").length;

  const refreshStatus = () => qc.invalidateQueries({ queryKey: domainStatusKey(project, service, envName) });

  // "Check again" asks the server to skip its 30s cache (older servers
  // ignore ?fresh and answer from cache, which is still a valid answer).
  const [rechecking, setRechecking] = useState(false);
  const checkAgain = async () => {
    setRechecking(true);
    try {
      const fresh = await fetchDomainStatus(project, service, envName, true);
      qc.setQueryData(domainStatusKey(project, service, envName), fresh);
    } catch {
      toast.error("Couldn't check the domains right now");
    } finally {
      setRechecking(false);
    }
  };

  const onRemove = async () => {
    if (!removing) return;
    try {
      await remove.mutateAsync(removing.host);
      toast.success(`${removing.host} removed`);
      setJustAdded((l) => l.filter((h) => h !== removing.host));
      void refreshStatus();
    } catch (e) {
      toastError(e, "Couldn't remove the domain");
    } finally {
      setRemoving(null);
    }
  };

  return (
    <Section
      id="domains"
      title="Domains"
      icon={Globe}
      status={customCount > 0 ? `${customCount} custom` : undefined}
      description={
        <>
          <Tooltip>
            <TooltipTrigger
              render={
                <span
                  tabIndex={0}
                  className="mr-1.5 inline-flex items-center gap-1 rounded-full border border-[var(--info)]/30 bg-[var(--info-subtle)] px-2 py-px align-[1px] text-xs font-medium text-[var(--info)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
                />
              }
            >
              <Zap className="h-3 w-3" aria-hidden />
              Changes apply immediately
            </TooltipTrigger>
            <TooltipContent className="max-w-64">
              Adding or removing a domain takes effect right away. Unlike the other cards, this one has no Save.
            </TooltipContent>
          </Tooltip>
          Where this service answers in the <Code>{envName}</Code> environment.
        </>
      }
    >
      {envLoading && !env ? (
        <p className="text-xs text-[var(--text-tertiary)]">Loading domains…</p>
      ) : !env ? (
        <Callout>
          This service has no <Code>{envName}</Code> environment yet, so there are no domains to manage here.
        </Callout>
      ) : (
        <>
          <ul className="divide-y divide-[var(--border-subtle)] overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]/40">
            {rows.map((r) => (
              <DomainRow
                key={`${r.kind}:${r.host}`}
                host={r.host}
                kind={r.kind}
                tlsSecret={r.tlsSecret}
                tlsEnabled={env.spec.tlsEnabled !== false}
                status={byHost.get(r.host.toLowerCase())}
                expectedIPs={status.data?.expectedIPs ?? []}
                checkedAt={status.data?.checkedAt}
                statusLoading={status.isLoading}
                statusUnavailable={statusUnavailable}
                forceOpen={justAdded.includes(r.host)}
                canWrite={canWrite}
                checking={rechecking}
                onCheck={() => void checkAgain()}
                onRemove={() => setRemoving({ host: r.host, kind: r.kind })}
              />
            ))}
          </ul>
          {statusUnavailable && (
            <p className="text-xs text-[var(--text-tertiary)]">
              {unsupported
                ? "Live DNS and certificate status needs a newer kuso server."
                : "Couldn't check DNS and certificate status right now."}
            </p>
          )}
          {canWrite &&
            (adding ? (
              <AddDomainForm
                envName={envName}
                existing={rows.map((r) => r.host.toLowerCase())}
                pending={add.isPending}
                onCancel={() => setAdding(false)}
                onAdd={async (host, tlsSecret) => {
                  try {
                    await add.mutateAsync({ host, ...(tlsSecret ? { tlsSecret } : {}) });
                    noteDomainAdd(host);
                    toast.success(`${host} added. Point its DNS at the cluster to finish.`);
                    setJustAdded((l) => [...l, host]);
                    setAdding(false);
                    void refreshStatus();
                    return null;
                  } catch (e) {
                    return e instanceof Error ? e.message : "Couldn't add the domain";
                  }
                }}
              />
            ) : (
              <div className="flex flex-wrap items-center gap-3">
                <Button type="button" variant="outline" size="sm" onClick={() => setAdding(true)}>
                  <Plus className="h-3.5 w-3.5" />
                  Add domain
                </Button>
                {customCount > 0 && (
                  <p className="text-xs text-[var(--text-secondary)]">
                    If your app redirects to the default domain, set its base-URL variable (<Code>APP_URL</Code>,{" "}
                    <Code>NEXTAUTH_URL</Code>…) to the custom domain.
                  </p>
                )}
              </div>
            ))}
        </>
      )}
      <ConfirmDialog
        open={removing !== null}
        title={`Remove ${removing?.host ?? "domain"}?`}
        body={
          <div className="space-y-2 text-[13px]">
            <p>
              <span className="font-mono">{removing?.host}</span> stops routing to <span className="font-mono">{service}</span>{" "}
              in <span className="font-mono">{envName}</span> right away.
            </p>
            {removing?.kind === "wildcard" ? (
              <p className="text-[var(--text-secondary)]">The TLS secret you provided is left in place.</p>
            ) : (
              <p className="text-[var(--text-secondary)]">
                Its certificate is deleted. Adding the domain back later requests a new one, which counts against
                Let&apos;s Encrypt&apos;s weekly limit for {removing ? registrableDomain(removing.host) : "the domain"}.
              </p>
            )}
          </div>
        }
        confirmLabel="Remove domain"
        pending={remove.isPending}
        onConfirm={() => void onRemove()}
        onCancel={() => setRemoving(null)}
      />
    </Section>
  );
}

// ----- status query -----

const domainStatusKey = (project: string, service: string, env: string) =>
  ["projects", project, "services", service, "envs", env, "domain-status"] as const;

function fetchDomainStatus(project: string, service: string, env: string, fresh = false) {
  return api<DomainStatusResponse>(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/envs/${encodeURIComponent(env)}/domains/status${fresh ? "?fresh=1" : ""}`,
  );
}

function useDomainStatus(project: string, service: string, env: string, enabled: boolean) {
  return useQuery({
    queryKey: domainStatusKey(project, service, env),
    queryFn: () => fetchDomainStatus(project, service, env),
    enabled: enabled && !!project && !!service,
    retry: false,
    staleTime: 10_000,
    meta: { skipGlobalErrorToast: true },
    // Poll faster while a domain is still waiting on DNS or its
    // certificate; the server caches results for 30s, so this can't
    // hammer DNS. An older server 404s: stop polling.
    refetchInterval: (q) => {
      if (q.state.error) return false;
      const pending = (q.state.data?.domains ?? []).some((d) => summarizeDomain(d).pending);
      return pending ? 15_000 : 60_000;
    },
    refetchIntervalInBackground: false,
  });
}

// ----- one domain -----

function DomainRow({
  host,
  kind,
  tlsSecret,
  tlsEnabled,
  status,
  expectedIPs,
  checkedAt,
  statusLoading,
  statusUnavailable,
  forceOpen,
  canWrite,
  checking,
  onCheck,
  onRemove,
}: {
  host: string;
  kind: DomainKind;
  tlsSecret?: string;
  tlsEnabled: boolean;
  status?: DomainStatus;
  expectedIPs: string[];
  checkedAt?: string;
  statusLoading: boolean;
  statusUnavailable: boolean;
  forceOpen: boolean;
  canWrite: boolean;
  checking: boolean;
  onCheck: () => void;
  onRemove: () => void;
}) {
  const summary = status ? summarizeDomain(status) : null;
  const diagnosis = status && kind !== "wildcard" && summary?.tone !== "ok" ? diagnose(status) : null;
  const resolve = status && kind !== "wildcard" ? resolveCheck(status, expectedIPs) : null;
  // The generated default domain is kuso's DNS; only custom domains need
  // the user to create records.
  const needsDns = kind === "custom";
  // Once DNS is confirmed pointing here the record table is noise; the
  // diagnosis explains whatever is still pending.
  const autoOpen = needsDns && (summary ? summary.pending && resolve?.state !== "match" : forceOpen);
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const open = userOpen ?? autoOpen;
  const url = `${hostScheme(tlsEnabled, host)}://${host}`;
  const Icon = kind === "wildcard" ? Lock : Globe;

  return (
    <li className="px-3 py-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div className="flex min-w-0 flex-1 basis-56 items-center gap-2">
          <Icon className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
          {kind === "wildcard" ? (
            <span className="min-w-0 truncate font-mono text-[13px] text-[var(--text-primary)]">{host}</span>
          ) : (
            <a
              href={url}
              target="_blank"
              rel="noopener noreferrer"
              title={`Open ${url}`}
              className="group inline-flex min-w-0 items-center gap-1 font-mono text-[13px] text-[var(--text-primary)] hover:underline"
            >
              <span className="truncate">{host}</span>
              <ExternalLink className="h-3 w-3 shrink-0 text-[var(--text-tertiary)] group-hover:text-[var(--text-primary)]" />
            </a>
          )}
          <KindBadge kind={kind} />
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          <StatusChip summary={summary} loading={statusLoading} unavailable={statusUnavailable} />
          <CopyButton value={url} label={`Copy ${url}`} />
          {needsDns && (
            <Button
              type="button"
              variant="ghost"
              size="xs"
              aria-expanded={open}
              onClick={() => setUserOpen(!open)}
            >
              {open ? "Hide DNS" : "DNS"}
            </Button>
          )}
          {canWrite && kind !== "auto" && (
            <button
              type="button"
              onClick={onRemove}
              aria-label={`Remove ${host}`}
              title="Remove domain"
              className="inline-flex h-7 w-7 items-center justify-center rounded-sm text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--error)]"
            >
              <Trash2 className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
      </div>
      {kind === "wildcard" && tlsSecret && (
        <p className="mt-1.5 pl-5.5 text-xs text-[var(--text-secondary)]">
          Served with TLS secret <Code>{tlsSecret}</Code>.
        </p>
      )}
      {!diagnosis && summary?.detail && summary.tone !== "ok" && kind !== "wildcard" && (
        <p
          className={cn(
            "mt-1.5 pl-5.5 text-xs",
            summary.tone === "error" ? "text-[var(--error)]" : "text-[var(--text-secondary)]",
          )}
        >
          {summary.detail}
        </p>
      )}
      {(diagnosis || (open && needsDns)) && (
        <div className="mt-3 space-y-3 sm:pl-5.5">
          {diagnosis && <DiagnosisNote diagnosis={diagnosis} />}
          {diagnosis && resolve && <ResolveLine host={host} check={resolve} />}
          {open && needsDns && <DnsInstructions host={host} />}
          {!statusUnavailable && (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
              <Button type="button" variant="outline" size="xs" onClick={onCheck} disabled={checking}>
                <RefreshCw className={cn("h-3 w-3", checking && "animate-spin")} />
                {checking ? "Checking…" : "Check again"}
              </Button>
              <span className="text-xs text-[var(--text-tertiary)]" aria-live="polite">
                {checking
                  ? `Looking up ${host}…`
                  : checkedAt
                    ? `Last checked ${formatClock(checkedAt)}${summary?.pending ? " · rechecks every 15 seconds" : ""}`
                    : "Rechecks automatically every 15 seconds."}
              </span>
            </div>
          )}
        </div>
      )}
    </li>
  );
}

function KindBadge({ kind }: { kind: DomainKind }) {
  const label = kind === "auto" ? "Default" : kind === "wildcard" ? "Wildcard" : null;
  if (!label) return null;
  return (
    <span className="shrink-0 rounded border border-[var(--border-subtle)] px-1.5 py-px text-xs text-[var(--text-secondary)]">
      {label}
    </span>
  );
}

const TONE_CLASS: Record<Tone, string> = {
  ok: "border-[var(--success)]/35 bg-[var(--success-subtle)] text-[var(--success)]",
  pending: "border-[var(--warning)]/35 bg-[var(--warning-subtle)] text-[var(--warning)]",
  error: "border-[var(--error)]/35 bg-[var(--error-subtle)] text-[var(--error)]",
  neutral: "border-[var(--border-subtle)] bg-[var(--bg-tertiary)] text-[var(--text-secondary)]",
};

function StatusChip({
  summary,
  loading,
  unavailable,
}: {
  summary: DomainSummary | null;
  loading: boolean;
  unavailable: boolean;
}) {
  if (!summary) {
    if (unavailable) return null;
    return (
      <span className={cn("rounded-full border px-2 py-0.5 text-xs", TONE_CLASS.neutral)}>
        {loading ? "Checking…" : "Not checked yet"}
      </span>
    );
  }
  return (
    <span className={cn("inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs", TONE_CLASS[summary.tone])}>
      {summary.tone === "ok" && <Check className="h-3 w-3" />}
      {summary.label}
    </span>
  );
}

const DIAG_STYLE = {
  error: { icon: AlertCircle, className: "text-[var(--error)]" },
  pending: { icon: Clock, className: "text-[var(--warning)]" },
  neutral: { icon: Info, className: "text-[var(--text-secondary)]" },
} as const;

// DiagnosisNote is the likely reason, in plain words, that the domain
// isn't serving HTTPS yet.
function DiagnosisNote({ diagnosis }: { diagnosis: NonNullable<ReturnType<typeof diagnose>> }) {
  const { icon: Icon, className } = DIAG_STYLE[diagnosis.tone];
  return (
    <div className="flex gap-2">
      <Icon className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", className)} aria-hidden />
      <div className="min-w-0 space-y-1">
        <p className="text-[13px] font-medium text-[var(--text-primary)]">{diagnosis.title}</p>
        <p className="text-xs leading-relaxed text-[var(--text-secondary)]">{diagnosis.body}</p>
        {diagnosis.record && (
          <dl className="flex flex-wrap gap-x-4 gap-y-1 pt-0.5 text-xs">
            {(
              [
                ["Type", diagnosis.record.type],
                ["Name", diagnosis.record.name],
                ["Value", diagnosis.record.value],
              ] as const
            ).map(([k, v]) => (
              <div key={k} className="flex items-center gap-1.5">
                <dt className="text-[var(--text-tertiary)]">{k}</dt>
                <dd className="inline-flex items-center gap-1 font-mono text-[var(--text-primary)]">
                  {v}
                  {k !== "Type" && <CopyButton value={v} label={`Copy ${k.toLowerCase()} ${v}`} size="xs" />}
                </dd>
              </div>
            ))}
          </dl>
        )}
        {diagnosis.raw && (
          <details className="text-xs text-[var(--text-tertiary)]">
            <summary className="cursor-pointer select-none hover:text-[var(--text-secondary)]">
              Details from cert-manager
            </summary>
            <p className="mt-1 font-mono break-words text-[var(--text-secondary)]">{diagnosis.raw}</p>
          </details>
        )}
      </div>
    </div>
  );
}

// ResolveLine is the live lookup result next to what kuso expects. The
// expected side prefers the public ingress targets (what the record
// table tells you to create) over the server's full set, which also
// lists private node addresses.
function ResolveLine({ host, check }: { host: string; check: ResolveCheck }) {
  const targets = useIngressTargets();
  const t = targets.data;
  const expected =
    t && t.source !== "none" ? (t.hostnames.length > 0 && t.ips.length === 0 ? t.hostnames : t.ips) : check.expected;
  const list = (xs: string[]) => {
    const shown = xs.slice(0, 3);
    return (
      <>
        {shown.map((x, i) => (
          <span key={x}>
            {i > 0 && ", "}
            <span className="font-mono text-[var(--text-primary)]">{x}</span>
          </span>
        ))}
        {xs.length > shown.length && ` +${xs.length - shown.length} more`}
      </>
    );
  };
  const tone =
    check.state === "match" ? "ok" : check.state === "elsewhere" ? "error" : check.state === "proxied" ? "neutral" : "pending";
  const Icon = check.state === "match" ? Check : check.state === "elsewhere" ? X : check.state === "proxied" ? Info : Clock;
  return (
    <div className="flex gap-2 text-xs text-[var(--text-secondary)]">
      <Icon
        className={cn(
          "mt-0.5 h-3.5 w-3.5 shrink-0",
          tone === "ok" && "text-[var(--success)]",
          tone === "error" && "text-[var(--error)]",
          tone === "pending" && "text-[var(--warning)]",
          tone === "neutral" && "text-[var(--text-tertiary)]",
        )}
        aria-hidden
      />
      <p className="min-w-0 leading-relaxed">
        {check.state === "unresolved" ? (
          <>
            <span className="font-mono text-[var(--text-primary)]">{host}</span> doesn&apos;t resolve yet
          </>
        ) : (
          <>Resolves to {list(check.resolved)}</>
        )}
        {check.state === "match" && " — that's this cluster."}
        {check.state === "proxied" && " (Cloudflare's proxy, so the origin can't be checked from DNS)."}
        {(check.state === "elsewhere" || check.state === "unresolved") &&
          (expected.length > 0 ? <>; expected {list(expected)}.</> : ".")}
      </p>
    </div>
  );
}

function formatClock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

// DnsInstructions shows the exact record(s) to create, with copy buttons.
function DnsInstructions({ host }: { host: string }) {
  const targets = useIngressTargets();
  const t = targets.data;
  if (targets.isLoading) return <p className="text-xs text-[var(--text-tertiary)]">Looking up the cluster address…</p>;
  if (!t || t.source === "none") {
    return (
      <Callout>
        Point <Code>{host}</Code> at your cluster&apos;s public address with an A record (or a CNAME to its load
        balancer). kuso couldn&apos;t detect that address automatically.
      </Callout>
    );
  }
  const records = suggestRecords(host, t);
  return (
    <div className="space-y-2">
      <p className="text-xs text-[var(--text-secondary)]">
        At your DNS provider, create {records.length > 1 ? "one of these records" : "this record"}:
      </p>
      <div className="overflow-x-auto rounded-md border border-[var(--border-subtle)]">
        <table className="w-full min-w-[360px] text-left text-xs">
          <thead className="bg-[var(--bg-tertiary)]/50 text-[var(--text-secondary)]">
            <tr>
              <th className="px-3 py-1.5 font-medium">Type</th>
              <th className="px-3 py-1.5 font-medium">Name</th>
              <th className="px-3 py-1.5 font-medium">Value</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--border-subtle)]">
            {records.map((r) => (
              <tr key={`${r.type}:${r.value}`}>
                <td className="px-3 py-1.5 font-mono text-[var(--text-primary)]">{r.type}</td>
                <td className="px-3 py-1.5">
                  <span className="inline-flex items-center gap-1 font-mono text-[var(--text-primary)]">
                    {r.name}
                    <CopyButton value={r.name} label={`Copy record name ${r.name}`} size="xs" />
                  </span>
                </td>
                <td className="px-3 py-1.5">
                  <span className="inline-flex items-center gap-1 font-mono text-[var(--text-primary)]">
                    {r.value}
                    <CopyButton value={r.value} label={`Copy ${r.value}`} size="xs" />
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {t.source === "nodes" && (
        <p className="text-xs text-[var(--text-secondary)]">
          These are node addresses (no load balancer found). Use one that is reachable from the internet.
        </p>
      )}
      <p className="text-xs text-[var(--text-secondary)]">
        Using Cloudflare? Either keep the record DNS-only until the certificate is issued, or set SSL to Full.
      </p>
    </div>
  );
}

// ----- add form -----

function AddDomainForm({
  envName,
  existing,
  pending,
  onCancel,
  onAdd,
}: {
  envName: string;
  existing: string[];
  pending: boolean;
  onCancel: () => void;
  // Resolves to an error message, or null on success.
  onAdd: (host: string, tlsSecret?: string) => Promise<string | null>;
}) {
  const [raw, setRaw] = useState("");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState<string | null>(null);
  const host = normalizeHostInput(raw);
  const wildcard = host.startsWith("*.");
  const validationError = raw.trim() ? hostError(host) : null;
  const duplicate = !!host && existing.includes(host);
  const ready = !!host && !validationError && !duplicate && (!wildcard || !!secret.trim());
  const recent = host && !validationError ? recentDomainAdds(registrableDomain(host)) : 0;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const err = hostError(host) ?? (duplicate ? `${host} is already on this environment` : null);
    if (err) {
      setError(err);
      return;
    }
    if (wildcard && !secret.trim()) {
      setError("Wildcard domains need the name of the TLS secret that holds their certificate");
      return;
    }
    setError(await onAdd(host, wildcard ? secret.trim() : undefined));
  };

  return (
    <form
      onSubmit={(e) => void submit(e)}
      className="space-y-4 rounded-md border border-[var(--border-default)] bg-[var(--bg-primary)]/50 p-4"
    >
      <div className="space-y-2">
        <label htmlFor="add-domain-host" className="block text-[13px] font-medium text-[var(--text-primary)]">
          Domain
        </label>
        <p className="text-xs text-[var(--text-secondary)]">
          Applies to the <Code>{envName}</Code> environment only. A certificate is issued automatically once DNS points
          here.
        </p>
        <Input
          id="add-domain-host"
          value={raw}
          onChange={(e) => {
            setRaw(e.target.value);
            setError(null);
          }}
          placeholder="e.g. app.example.com"
          autoFocus
          spellCheck={false}
          autoComplete="off"
          aria-invalid={!!(validationError || duplicate)}
          className={monoInputClass}
        />
        {(validationError || duplicate) && (
          <p className="text-xs text-[var(--error)]">
            {validationError ?? `${host} is already on this environment`}
          </p>
        )}
      </div>
      {wildcard && (
        <div className="space-y-2">
          <label htmlFor="add-domain-secret" className="block text-[13px] font-medium text-[var(--text-primary)]">
            TLS secret
          </label>
          <p className="text-xs text-[var(--text-secondary)]">
            Let&apos;s Encrypt can&apos;t issue wildcards over HTTP, so name a secret in the project namespace that
            already holds the certificate (e.g. minted by a cert-manager DNS-01 Certificate).
          </p>
          <Input
            id="add-domain-secret"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            placeholder="e.g. wildcard-example-tls"
            spellCheck={false}
            className={monoInputClass}
          />
        </div>
      )}
      {host && !validationError && !wildcard && (
        <>
          <DnsInstructions host={host} />
          <HostClashHint hosts={[host]} />
        </>
      )}
      {!wildcard && (
        <Callout tone={recent >= BUDGET_WARN_AT ? "warning" : "info"}>
          {recent >= BUDGET_WARN_AT ? (
            <>
              You&apos;ve added {recent} domains under <Code>{registrableDomain(host)}</Code> in the last hour.{" "}
            </>
          ) : null}
          Create the DNS record first, then add the domain. Let&apos;s Encrypt allows 5 failed validations per domain
          per hour and 50 certificates per registered domain per week, so adding a domain before its DNS is ready, or
          removing and re-adding it, can lock you out for a while.
        </Callout>
      )}
      {error && (
        <p role="alert" className="text-xs text-[var(--error)]">
          {error}
        </p>
      )}
      <div className="flex items-center justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onCancel} disabled={pending}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={!ready || pending}>
          {pending ? "Adding…" : "Add domain"}
        </Button>
      </div>
    </form>
  );
}

// ----- small pieces -----

const BUDGET_KEY = "kuso.domainAdds";

function readBudget(): Record<string, number[]> {
  try {
    const raw = window.localStorage.getItem(BUDGET_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : {};
    if (!parsed || typeof parsed !== "object") return {};
    const out: Record<string, number[]> = {};
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (Array.isArray(v)) out[k] = v.filter((n): n is number => typeof n === "number");
    }
    return out;
  } catch {
    return {};
  }
}

function recentDomainAdds(root: string): number {
  return recentAdds(readBudget()[root] ?? [], Date.now()).length;
}

function noteDomainAdd(host: string) {
  const root = registrableDomain(host);
  const all = readBudget();
  all[root] = [...recentAdds(all[root] ?? [], Date.now()), Date.now()];
  try {
    window.localStorage.setItem(BUDGET_KEY, JSON.stringify(all));
  } catch {
    // Storage unavailable (private mode): the warning just won't escalate.
  }
}
