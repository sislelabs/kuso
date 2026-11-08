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
  redactUrlCredentials,
  useCreateDrain,
  useDeleteDrain,
  useDrains,
  useTestDrain,
  type Drain,
  type DrainType,
} from "@/features/drains";
import { Pencil, Plus, Send, Trash2 } from "lucide-react";
import { DrainEditForm } from "@/components/settings/DrainEditForm";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { SettingsPage } from "@/components/settings/SettingsPage";
import {
  SettingsField,
  SettingsNotice,
  SettingsSection,
  SettingsSelect,
} from "@/components/settings/SettingsSection";

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

const TYPE_LABELS = Object.fromEntries(
  (Object.keys(TYPE_HINTS) as DrainType[]).map((t) => [t, TYPE_HINTS[t].label])
) as Record<DrainType, string>;

export default function DrainsPage() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const list = useDrains(isAdmin);
  const create = useCreateDrain();
  const remove = useDeleteDrain();
  const test = useTestDrain();
  const [pendingDelete, setPendingDelete] = useState<Drain | null>(null);
  const [testingId, setTestingId] = useState<string | null>(null);
  const [editingId, setEditingId] = useState<string | null>(null);

  const [type, setType] = useState<DrainType>("otlp");
  const [url, setUrl] = useState("");
  const [name, setName] = useState("");
  const [project, setProject] = useState("");
  const [headersText, setHeadersText] = useState("");
  const [secret, setSecret] = useState("");

  if (!isAdmin) {
    return (
      <SettingsPage title="Log & metrics drains">
        <SettingsNotice tone="neutral" title="Admin only">
          Log drains need <span className="font-mono">settings:admin</span>: a drain ships every
          matching log line off-cluster, along with the credentials to do it.
        </SettingsNotice>
      </SettingsPage>
    );
  }

  const onTest = (d: Drain) => {
    setTestingId(d.id);
    test.mutate(d.id, {
      onSuccess: (r) => toast.success(`${d.name}: upstream accepted the test line (${r.status})`),
      onError: (e) => toastError(e, "Test failed"),
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
        onError: (e) => toastError(e, "Save failed"),
      }
    );
  };

  const drains = list.data ?? [];

  return (
    <SettingsPage
      title="Log & metrics drains"
      description={
        <>
          Forward every app log line to Grafana, an OpenTelemetry collector, Loki or your own
          endpoint, for all projects or just one. Lines are batched and retried; a sink that falls
          behind has lines dropped rather than slowing collection (
          <code className="font-mono text-xs">kuso_drain_lines_dropped_total</code>). Changes apply
          within ~30s.
        </>
      }
    >
      <SettingsSection
        title="Log drains"
        description={
          list.isSuccess
            ? `${drains.length} ${drains.length === 1 ? "drain" : "drains"} configured.`
            : undefined
        }
        flush={list.isSuccess && drains.length > 0}
      >
        {list.isPending ? (
          <Skeleton className="h-16 w-full" />
        ) : list.isError ? (
          <QueryErrorState what="drains" error={list.error} onRetry={() => void list.refetch()} />
        ) : drains.length === 0 ? (
          <EmptyState title="No drains yet" description="Add one below." className="px-3 py-8" />
        ) : (
          <ul className="divide-y divide-[var(--border-subtle)]">
            {drains.map((d) => (
              <li key={d.id}>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3 sm:flex-nowrap sm:px-5">
                  <div className="min-w-0 flex-1 basis-full sm:basis-auto">
                    <div className="flex items-center gap-2">
                      <span className="truncate text-sm font-medium text-[var(--text-primary)]">{d.name}</span>
                      <span className="shrink-0 rounded-sm bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-xs text-[var(--text-secondary)]">
                        {TYPE_HINTS[d.type]?.label ?? d.type}
                      </span>
                      {!d.enabled && (
                        <span className="shrink-0 text-xs text-[var(--text-tertiary)]">Paused</span>
                      )}
                    </div>
                    <p className="mt-0.5 truncate text-xs text-[var(--text-secondary)]">
                      {d.project ? (
                        <>
                          Project <span className="font-mono">{d.project}</span>
                        </>
                      ) : (
                        "All projects"
                      )}{" "}
                      · <span className="font-mono">{redactUrlCredentials(d.url)}</span>
                      {d.headers && Object.keys(d.headers).length > 0 && (
                        <>
                          {" "}
                          · headers <span className="font-mono">{Object.keys(d.headers).join(", ")}</span>
                        </>
                      )}
                      {d.secret && " · signed"}
                    </p>
                  </div>
                  <div className="ml-auto flex shrink-0 items-center gap-1">
                    <Button size="sm" variant="outline" disabled={testingId !== null} onClick={() => onTest(d)}>
                      <Send className="h-3.5 w-3.5" />
                      {testingId === d.id ? "Sending…" : "Test"}
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => setEditingId(editingId === d.id ? null : d.id)}
                      aria-label={`Edit ${d.name}`}
                      aria-expanded={editingId === d.id}
                    >
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => setPendingDelete(d)}
                      disabled={remove.isPending}
                      className="hover:text-[var(--error)]"
                      aria-label={`Delete ${d.name}`}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </div>
                {editingId === d.id && (
                  <DrainEditForm
                    drain={d}
                    typeLabels={TYPE_LABELS}
                    onDone={() => setEditingId(null)}
                  />
                )}
              </li>
            ))}
          </ul>
        )}
      </SettingsSection>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit();
        }}
      >
        <SettingsSection
          title="Add log drain"
          description="Send a test line after adding to confirm the sink accepts it."
          footer={
            <Button size="sm" type="submit" disabled={!url.trim() || create.isPending}>
              <Plus className="h-3.5 w-3.5" />
              {create.isPending ? "Saving…" : "Add drain"}
            </Button>
          }
        >
          <div className="grid grid-cols-1 gap-5 sm:grid-cols-[12rem_1fr]">
            <SettingsField label="Type" htmlFor="drain-type">
              <SettingsSelect
                id="drain-type"
                value={type}
                onChange={(e) => setType(e.target.value as DrainType)}
              >
                {(Object.keys(TYPE_HINTS) as DrainType[]).map((t) => (
                  <option key={t} value={t}>{TYPE_HINTS[t].label}</option>
                ))}
              </SettingsSelect>
            </SettingsField>
            <SettingsField label="URL" htmlFor="drain-url" hint={TYPE_HINTS[type].hint}>
              <Input
                id="drain-url"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder={TYPE_HINTS[type].placeholder}
                className="font-mono"
                spellCheck={false}
                autoComplete="off"
              />
            </SettingsField>
          </div>
          <div className="grid grid-cols-1 gap-5 sm:grid-cols-2">
            <SettingsField label="Name" htmlFor="drain-name" hint="Optional. Defaults to the host.">
              <Input id="drain-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. grafana cloud" />
            </SettingsField>
            <SettingsField label="Project" htmlFor="drain-project" hint="Leave blank to drain every project.">
              <Input
                id="drain-project"
                value={project}
                onChange={(e) => setProject(e.target.value)}
                placeholder="All projects"
                className="font-mono"
                spellCheck={false}
              />
            </SettingsField>
          </div>
          <SettingsField label="Headers" htmlFor="drain-headers" hint="One per line, as Name: value.">
            <textarea
              id="drain-headers"
              value={headersText}
              onChange={(e) => setHeadersText(e.target.value)}
              rows={2}
              spellCheck={false}
              placeholder={"Authorization: Bearer …\nX-Scope-OrgID: tenant-1"}
              className="w-full resize-y rounded-sm border border-border bg-input px-3 py-2 font-mono text-xs text-[var(--text-primary)] outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
            />
          </SettingsField>
          <SettingsField label="HMAC secret" htmlFor="drain-secret" hint="Optional. Signs each batch with X-Kuso-Signature.">
            <Input
              id="drain-secret"
              type="password"
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              className="font-mono"
              autoComplete="new-password"
            />
          </SettingsField>
        </SettingsSection>
      </form>

      <SettingsSection
        title="Metrics"
        description={
          <>
            Metrics are pulled, not pushed. Point Grafana Alloy, Prometheus or Grafana Cloud&apos;s
            Metrics Endpoint at{" "}
            <code className="rounded-sm bg-[var(--bg-tertiary)] px-1 font-mono text-xs">/api/metrics/export</code>{" "}
            with an admin API token (or <code className="font-mono text-xs">KUSO_METRICS_SCRAPE_TOKEN</code>)
            as a bearer token. It serves per-env request rate, 5xx rate, p95 latency, CPU, memory
            and pod count, labelled project / service / env.
          </>
        }
      >
        <pre className="overflow-x-auto rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3 font-mono text-xs text-[var(--text-secondary)]">
{`scrape_configs:
  - job_name: kuso
    scheme: https
    metrics_path: /api/metrics/export
    authorization: { credentials: <token> }
    static_configs: [{ targets: ["<your-kuso-host>"] }]`}
        </pre>
      </SettingsSection>

      <ConfirmDialog
        open={pendingDelete !== null}
        title={`Delete log drain ${pendingDelete?.name ?? ""}?`}
        body={
          <p>
            <span className="font-mono text-[var(--text-primary)]">{pendingDelete?.name}</span> stops receiving logs
            within ~30s. Its credentials can&apos;t be recovered — you&apos;ll need to re-enter them to add it back.
          </p>
        }
        confirmLabel="Delete drain"
        typeToConfirm={pendingDelete?.name}
        destructive
        pending={remove.isPending}
        onConfirm={() => {
          if (pendingDelete) {
            remove.mutate(pendingDelete.id, {
              onError: (e) => toastError(e, "Delete failed"),
            });
          }
          setPendingDelete(null);
        }}
        onCancel={() => setPendingDelete(null)}
      />
    </SettingsPage>
  );
}
