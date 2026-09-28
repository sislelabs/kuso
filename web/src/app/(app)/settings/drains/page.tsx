"use client";

import { useState } from "react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { EmptyState } from "@/components/shared/EmptyState";
import { useCan, Perms } from "@/features/auth";
import {
  parseHeaderLines,
  useCreateDrain,
  useDeleteDrain,
  useDrains,
  useTestDrain,
  type Drain,
  type DrainType,
} from "@/features/drains";
import { Plus, Send, Trash2, Waves } from "lucide-react";
import { toast } from "sonner";

// /settings/drains — forward app logs to external sinks, plus the
// scrape endpoint for metrics. Admin-only: a drain ships every matching
// log line off-cluster along with the credentials to do it.

const TYPE_HINTS: Record<DrainType, { label: string; placeholder: string; hint: string }> = {
  otlp: {
    label: "OTLP / HTTP",
    placeholder: "https://otlp-gateway-prod-eu-west-2.grafana.net/otlp",
    hint: "JSON-encoded OTLP logs. /v1/logs is appended to a base URL.",
  },
  loki: {
    label: "Loki",
    placeholder: "https://USER:TOKEN@logs-prod-012.grafana.net",
    hint: "Loki push API, labels project/service/env. /loki/api/v1/push is appended; user:token in the URL becomes Basic auth.",
  },
  http: {
    label: "HTTP JSON",
    placeholder: "https://logs.example.com/ingest",
    hint: "POSTs a JSON array of {ts, project, service, env, pod, stream, line}. A secret adds X-Kuso-Signature HMAC headers.",
  },
};

const labelCls = "mb-1 block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]";

export default function DrainsPage() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const list = useDrains(isAdmin);
  const create = useCreateDrain();
  const remove = useDeleteDrain();
  const test = useTestDrain();
  const [pendingDelete, setPendingDelete] = useState<Drain | null>(null);
  const [testingId, setTestingId] = useState<string | null>(null);

  const [type, setType] = useState<DrainType>("otlp");
  const [url, setUrl] = useState("");
  const [name, setName] = useState("");
  const [project, setProject] = useState("");
  const [headersText, setHeadersText] = useState("");
  const [secret, setSecret] = useState("");

  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-3xl p-6 lg:p-8">
        <p className="rounded-md border border-[var(--warning)]/30 bg-[var(--warning-subtle)] p-4 text-sm text-[var(--warning)]">
          Log drains are admin-only.
        </p>
      </div>
    );
  }

  const onTest = (d: Drain) => {
    setTestingId(d.id);
    test.mutate(d.id, {
      onSuccess: (r) => toast.success(`${d.name}: upstream accepted the test line (${r.status})`),
      onError: (e) => toast.error(e instanceof Error ? e.message : "Test failed"),
      onSettled: () => setTestingId(null),
    });
  };

  const onSubmit = () => {
    const parsed = parseHeaderLines(headersText);
    if (!parsed.ok) {
      toast.error(`Header ${parsed.error}`);
      return;
    }
    create.mutate(
      { type, url: url.trim(), name: name.trim(), project: project.trim(), headers: parsed.headers, secret },
      {
        onSuccess: (d) => {
          toast.success(`Drain ${d.name} added — send a test line to verify it`);
          setUrl("");
          setName("");
          setProject("");
          setHeadersText("");
          setSecret("");
        },
        onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
      }
    );
  };

  const drains = list.data ?? [];

  return (
    <div className="mx-auto max-w-3xl p-6 lg:p-8">
      <header className="mb-6 flex items-start gap-3">
        <Waves className="mt-1 h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">Log & metrics drains</h1>
          <p className="mt-1 text-[12px] leading-relaxed text-[var(--text-secondary)]">
            Forward every app log line kuso collects to Grafana, an OpenTelemetry collector, Loki or your own endpoint —
            for all projects or just one. Lines are batched and retried; if a sink falls behind, kuso drops lines for
            that sink rather than slowing log collection (<code className="font-mono text-[11px]">kuso_drain_lines_dropped_total</code>).
            Config changes apply within ~30s.
          </p>
        </div>
      </header>

      <section className="space-y-3">
        <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          log drains ({drains.length})
        </h2>
        {list.isPending ? (
          <Skeleton className="h-16 w-full" />
        ) : list.isError ? (
          <p className="font-mono text-[11px] text-[var(--error)]">
            {list.error instanceof Error ? list.error.message : "Failed to load drains"}
          </p>
        ) : drains.length === 0 ? (
          <EmptyState title="No drains yet" description="Add one below." className="px-3 py-8" />
        ) : (
          <ul className="overflow-hidden rounded-md border border-[var(--border-subtle)]">
            {drains.map((d) => (
              <li
                key={d.id}
                className="flex items-center gap-3 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-2 last:border-b-0"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="truncate text-[13px] text-[var(--text-primary)]">{d.name}</span>
                    <span className="rounded bg-[var(--bg-tertiary)] px-1.5 font-mono text-[10px] text-[var(--text-secondary)]">
                      {TYPE_HINTS[d.type]?.label ?? d.type}
                    </span>
                    {!d.enabled && (
                      <span className="font-mono text-[10px] text-[var(--text-tertiary)]">paused</span>
                    )}
                  </div>
                  <p className="truncate font-mono text-[11px] text-[var(--text-tertiary)]">
                    {d.project ? `project ${d.project}` : "all projects"} · {d.url}
                    {d.headers && Object.keys(d.headers).length > 0 && ` · headers: ${Object.keys(d.headers).join(", ")}`}
                    {d.secret && " · signed"}
                  </p>
                </div>
                <Button size="sm" variant="outline" disabled={testingId !== null} onClick={() => onTest(d)}>
                  <Send className="h-3.5 w-3.5" />
                  {testingId === d.id ? "Sending…" : "Test"}
                </Button>
                <button
                  type="button"
                  onClick={() => setPendingDelete(d)}
                  disabled={remove.isPending}
                  className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--error-subtle)] hover:text-[var(--error)] disabled:opacity-40"
                  aria-label={`Delete ${d.name}`}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="mt-8 space-y-3">
        <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">add log drain</h2>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            onSubmit();
          }}
          className="flex flex-col gap-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-3"
        >
          <div className="flex flex-col gap-2 sm:flex-row">
            <div className="sm:w-40">
              <label htmlFor="drain-type" className={labelCls}>type</label>
              <select
                id="drain-type"
                value={type}
                onChange={(e) => setType(e.target.value as DrainType)}
                className="h-8 w-full rounded-sm border border-[var(--border-subtle)] bg-[var(--input)] px-2 font-mono text-[12px]"
              >
                {(Object.keys(TYPE_HINTS) as DrainType[]).map((t) => (
                  <option key={t} value={t}>{TYPE_HINTS[t].label}</option>
                ))}
              </select>
            </div>
            <div className="flex-1">
              <label htmlFor="drain-url" className={labelCls}>url</label>
              <Input
                id="drain-url"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder={TYPE_HINTS[type].placeholder}
                className="h-8 font-mono text-[12px]"
                spellCheck={false}
                autoComplete="off"
              />
            </div>
          </div>
          <p className="font-mono text-[10px] text-[var(--text-tertiary)]">{TYPE_HINTS[type].hint}</p>
          <div className="flex flex-col gap-2 sm:flex-row">
            <div className="flex-1">
              <label htmlFor="drain-name" className={labelCls}>name (optional)</label>
              <Input id="drain-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="grafana cloud" className="h-8 text-[12px]" />
            </div>
            <div className="flex-1">
              <label htmlFor="drain-project" className={labelCls}>project (blank = all)</label>
              <Input
                id="drain-project"
                value={project}
                onChange={(e) => setProject(e.target.value)}
                placeholder="all projects"
                className="h-8 font-mono text-[12px]"
                spellCheck={false}
              />
            </div>
          </div>
          <div>
            <label htmlFor="drain-headers" className={labelCls}>headers (one per line)</label>
            <textarea
              id="drain-headers"
              value={headersText}
              onChange={(e) => setHeadersText(e.target.value)}
              rows={2}
              spellCheck={false}
              placeholder={"Authorization: Bearer …\nX-Scope-OrgID: tenant-1"}
              className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-[11px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
            />
          </div>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
            <div className="flex-1">
              <label htmlFor="drain-secret" className={labelCls}>hmac secret (optional)</label>
              <Input
                id="drain-secret"
                type="password"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                className="h-8 font-mono text-[12px]"
                autoComplete="new-password"
              />
            </div>
            <Button size="sm" type="submit" disabled={!url.trim() || create.isPending}>
              <Plus className="h-3.5 w-3.5" />
              {create.isPending ? "Saving…" : "Add drain"}
            </Button>
          </div>
        </form>
      </section>

      <section className="mt-8 space-y-2">
        <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">metrics</h2>
        <p className="text-[12px] leading-relaxed text-[var(--text-secondary)]">
          Metrics are pulled, not pushed. Point Grafana Alloy, Prometheus or Grafana Cloud&apos;s Metrics Endpoint
          integration at{" "}
          <code className="rounded bg-[var(--bg-secondary)] px-1 font-mono text-[11px]">/api/metrics/export</code> with an
          admin API token (or <code className="font-mono text-[11px]">KUSO_METRICS_SCRAPE_TOKEN</code>) as a bearer token.
          It serves per-env request rate, 5xx rate, p95 latency, CPU, memory and pod count, labelled project / service / env.
        </p>
        <pre className="overflow-x-auto rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3 font-mono text-[11px] text-[var(--text-secondary)]">
{`scrape_configs:
  - job_name: kuso
    scheme: https
    metrics_path: /api/metrics/export
    authorization: { credentials: <token> }
    static_configs: [{ targets: ["<your-kuso-host>"] }]`}
        </pre>
      </section>

      <ConfirmDialog
        open={pendingDelete !== null}
        title="Delete log drain?"
        body={
          <p>
            <span className="font-mono text-[var(--text-primary)]">{pendingDelete?.name}</span> stops receiving logs
            within ~30s. Its credentials can&apos;t be recovered — you&apos;ll need to re-enter them to add it back.
          </p>
        }
        confirmLabel="Delete drain"
        destructive
        pending={remove.isPending}
        onConfirm={() => {
          if (pendingDelete) {
            remove.mutate(pendingDelete.id, {
              onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
            });
          }
          setPendingDelete(null);
        }}
        onCancel={() => setPendingDelete(null)}
      />
    </div>
  );
}
