"use client";

import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight, ExternalLink, Siren } from "lucide-react";
import { toast } from "sonner";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import {
  INCIDENT_STATES,
  isOpenIncident,
  useIncident,
  useIncidentFeedback,
  useIncidents,
  useResolveIncident,
  type Incident,
} from "@/features/incidents";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

// "" = every state; "open" is filtered client-side because the server
// only accepts a single ?state=.
type Filter = "" | "open" | (typeof INCIDENT_STATES)[number];

const STATE_TONE: Record<string, string> = {
  investigating: "border-[var(--info)]/30 bg-[var(--info-subtle)] text-[var(--info)]",
  awaiting_feedback: "border-[var(--warning)]/30 bg-[var(--warning-subtle)] text-[var(--warning)]",
  implementing: "border-[var(--info)]/30 bg-[var(--info-subtle)] text-[var(--info)]",
  pr_open: "border-[var(--accent)]/30 text-[var(--accent)]",
  resolved: "border-[var(--success)]/30 bg-[var(--success-subtle)] text-[var(--success)]",
};

function StatePill({ state }: { state: string }) {
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded-full border px-2 py-0.5 font-mono text-[10px]",
        STATE_TONE[state] ?? "border-[var(--border-subtle)] text-[var(--text-tertiary)]",
      )}
    >
      {state.replace(/_/g, " ")}
    </span>
  );
}

// IncidentsSection is the operator view of the incident agent's work:
// list, detail, approve/reject/comment, resolve. Admin-only server-side.
export function IncidentsSection({ canAdmin }: { canAdmin: boolean }) {
  const [filter, setFilter] = useState<Filter>("open");
  const [selected, setSelected] = useState<string | null>(null);
  const serverState = filter === "open" ? "" : filter;
  const list = useIncidents(serverState, canAdmin);

  const rows = useMemo(() => {
    const all = (list.data?.pages ?? []).flat();
    return filter === "open" ? all.filter((i) => isOpenIncident(i.state)) : all;
  }, [list.data, filter]);

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-3 space-y-0">
        <CardTitle className="flex items-center gap-2 text-sm">
          <Siren className="h-4 w-4 text-[var(--accent)]" />
          Incidents
        </CardTitle>
        {canAdmin && (
          <select
            value={filter}
            onChange={(e) => {
              setFilter(e.target.value as Filter);
              setSelected(null);
            }}
            aria-label="Filter incidents by state"
            className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px]"
          >
            <option value="open">open</option>
            <option value="">all states</option>
            {INCIDENT_STATES.map((s) => (
              <option key={s} value={s}>
                {s.replace(/_/g, " ")}
              </option>
            ))}
          </select>
        )}
      </CardHeader>
      <CardContent className="space-y-3 text-[13px]">
        {!canAdmin ? (
          <p className="text-[11px] text-[var(--text-tertiary)]">Only instance admins can view incidents.</p>
        ) : list.isPending ? (
          <Skeleton className="h-24" />
        ) : list.isError ? (
          <QueryErrorState what="incidents" error={list.error} onRetry={() => void list.refetch()} />
        ) : rows.length === 0 ? (
          <p className="py-4 text-center text-[12px] text-[var(--text-tertiary)]">
            {filter === "open"
              ? "No open incidents."
              : filter
                ? `No ${filter.replace(/_/g, " ")} incidents.`
                : "No incidents recorded yet."}
            {filter === "open" && list.hasNextPage ? " Older pages may hold more; switch to “all states”." : ""}
          </p>
        ) : (
          <ul className="divide-y divide-[var(--border-subtle)] overflow-hidden rounded-md border border-[var(--border-subtle)]">
            {rows.map((i) => (
              <IncidentRow
                key={i.id}
                incident={i}
                open={selected === i.id}
                onToggle={() => setSelected(selected === i.id ? null : i.id)}
              />
            ))}
          </ul>
        )}
        {canAdmin && list.hasNextPage && (
          <div className="flex justify-center">
            <Button
              size="sm"
              variant="outline"
              disabled={list.isFetchingNextPage}
              onClick={() => void list.fetchNextPage()}
            >
              {list.isFetchingNextPage ? "Loading…" : "Load older"}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function IncidentRow({
  incident,
  open,
  onToggle,
}: {
  incident: Incident;
  open: boolean;
  onToggle: () => void;
}) {
  const target = [incident.project, incident.service].filter(Boolean).join("/") || incident.targetKey;
  return (
    <li className="bg-[var(--bg-primary)]">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-[var(--bg-tertiary)]/40"
      >
        {open ? (
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
        )}
        <div className="min-w-0 flex-1">
          <p className="truncate text-[12px] text-[var(--text-primary)]">{incident.title || incident.eventType}</p>
          <p className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">
            {target} · {incident.eventType}
            {incident.severity ? ` · ${incident.severity}` : ""} · {relativeTime(incident.createdAt)}
          </p>
        </div>
        <StatePill state={incident.state} />
      </button>
      {open && <IncidentDetail id={incident.id} />}
    </li>
  );
}

type Pending = "go" | "reject" | "resolve" | null;

function IncidentDetail({ id }: { id: string }) {
  const detail = useIncident(id);
  const feedback = useIncidentFeedback();
  const resolve = useResolveIncident();
  const [comment, setComment] = useState("");
  const [confirm, setConfirm] = useState<Pending>(null);

  if (detail.isPending) return <Skeleton className="m-3 h-24" />;
  if (detail.isError)
    return <QueryErrorState className="m-3" what="incident" error={detail.error} onRetry={() => void detail.refetch()} />;

  const inc = detail.data;
  const open = isOpenIncident(inc.state);
  const busy = feedback.isPending || resolve.isPending;
  const onErr = (e: unknown) => toast.error(e instanceof Error ? e.message : "request failed");

  const runConfirmed = () => {
    const action = confirm;
    setConfirm(null);
    if (action === "resolve") {
      resolve.mutate(id, { onSuccess: () => toast.success("Incident resolved"), onError: onErr });
    } else if (action === "go" || action === "reject") {
      feedback.mutate(
        { id, body: { decision: action } },
        {
          onSuccess: () => toast.success(action === "go" ? "Approved — implement agent starting" : "Incident rejected"),
          onError: onErr,
        },
      );
    }
  };

  const contextJSON = inc.contextPack == null ? "" : JSON.stringify(inc.contextPack, null, 2);

  return (
    <div className="space-y-3 border-t border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-3 text-[12px]">
      <dl className="grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1 font-mono text-[11px]">
        <dt className="text-[var(--text-tertiary)]">id</dt>
        <dd className="break-all text-[var(--text-secondary)]">{inc.id}</dd>
        <dt className="text-[var(--text-tertiary)]">opened</dt>
        <dd className="text-[var(--text-secondary)]">{new Date(inc.createdAt).toLocaleString()}</dd>
        {inc.closedAt && (
          <>
            <dt className="text-[var(--text-tertiary)]">closed</dt>
            <dd className="text-[var(--text-secondary)]">{new Date(inc.closedAt).toLocaleString()}</dd>
          </>
        )}
        {inc.prUrl && (
          <>
            <dt className="text-[var(--text-tertiary)]">pull request</dt>
            <dd>
              <a
                href={inc.prUrl}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 text-[var(--accent)] hover:underline"
              >
                #{inc.prNumber || "PR"} <ExternalLink className="h-3 w-3" />
              </a>
            </dd>
          </>
        )}
        {(inc.investigateJob || inc.implementJob) && (
          <>
            <dt className="text-[var(--text-tertiary)]">jobs</dt>
            <dd className="break-all text-[var(--text-secondary)]">
              {[inc.investigateJob, inc.implementJob].filter(Boolean).join(", ")}
            </dd>
          </>
        )}
      </dl>

      <div>
        <h4 className="mb-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">Findings</h4>
        {inc.findings ? (
          <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-[11px] text-[var(--text-secondary)]">
            {inc.findings}
          </pre>
        ) : (
          <p className="text-[11px] text-[var(--text-tertiary)]">
            {inc.state === "investigating" ? "The agent is still investigating." : "No findings were posted."}
          </p>
        )}
      </div>

      {(inc.feedback ?? []).length > 0 && (
        <div>
          <h4 className="mb-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">Feedback</h4>
          <ul className="space-y-1">
            {(inc.feedback ?? []).map((f, idx) => (
              <li key={`${f.at}-${idx}`} className="text-[11px] text-[var(--text-secondary)]">
                <span className="font-mono text-[var(--text-tertiary)]">{relativeTime(f.at)} · </span>
                {f.decision ? <span className="font-mono">decision: {f.decision}</span> : f.text}
              </li>
            ))}
          </ul>
        </div>
      )}

      {contextJSON && (
        <details>
          <summary className="cursor-pointer font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            Context pack
          </summary>
          <pre className="mt-1 max-h-64 overflow-auto rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-[10px] text-[var(--text-secondary)]">
            {contextJSON}
          </pre>
        </details>
      )}

      {open && (
        <div className="space-y-2">
          <textarea
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            rows={2}
            aria-label="Feedback for the agent"
            placeholder="Reply to the agent (recorded on the incident)…"
            className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 text-[12px] outline-none focus:border-[var(--border-strong)]"
          />
          <div className="flex flex-wrap items-center justify-end gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={busy || !comment.trim()}
              onClick={() =>
                feedback.mutate(
                  { id, body: { text: comment.trim() } },
                  {
                    onSuccess: () => {
                      toast.success("Feedback recorded");
                      setComment("");
                    },
                    onError: onErr,
                  },
                )
              }
            >
              Send feedback
            </Button>
            {inc.state === "awaiting_feedback" && (
              <>
                <Button size="sm" variant="outline" disabled={busy} onClick={() => setConfirm("reject")}>
                  Reject
                </Button>
                <Button size="sm" disabled={busy} onClick={() => setConfirm("go")}>
                  Approve fix
                </Button>
              </>
            )}
            <Button size="sm" variant="outline" disabled={busy} onClick={() => setConfirm("resolve")}>
              Resolve
            </Button>
          </div>
        </div>
      )}

      <ConfirmDialog
        open={confirm !== null}
        title={
          confirm === "go" ? "Approve the proposed fix?" : confirm === "reject" ? "Reject this incident?" : "Resolve this incident?"
        }
        destructive={confirm === "reject"}
        confirmLabel={confirm === "go" ? "Approve" : confirm === "reject" ? "Reject" : "Resolve"}
        body={
          confirm === "go" ? (
            <span>Starts the implement agent, which writes the fix and opens a pull request.</span>
          ) : confirm === "reject" ? (
            <span>Closes the incident as rejected. The agent won&apos;t act on it.</span>
          ) : (
            <span>Closes the incident as resolved.</span>
          )
        }
        pending={busy}
        onConfirm={runConfirmed}
        onCancel={() => setConfirm(null)}
      />
    </div>
  );
}
