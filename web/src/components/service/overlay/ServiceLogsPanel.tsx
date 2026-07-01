"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { searchServiceLogs } from "@/features/services";
import type { LogLine, LogSearchResponse } from "@/features/services";
import { useEnvironments } from "@/features/projects";
import { Search, X } from "lucide-react";
import { cn } from "@/lib/utils";

interface Props {
  project: string;
  service: string;
}

// ServiceLogsPanel — full-text search over the FTS5-backed log
// archive populated by the kuso-server logship goroutine. Default
// view shows the latest 200 lines from the last hour with no
// query; typing in the search box switches to an FTS5 MATCH query.
//
// Time-range picker is two preset chips ("1h", "6h", "24h", "7d")
// plus custom RFC3339 inputs hidden behind a toggle. Indie SaaS
// founders mostly want "what just happened" and a relative range
// is the right default.
export function ServiceLogsPanel({ project, service }: Props) {
  const [q, setQ] = useState("");
  const [env, setEnv] = useState("");
  const [since, setSince] = useState("1h");
  const [committed, setCommitted] = useState({ q: "", env: "", since: "1h" });

  // Build the env-filter list from the actual envs that exist for
  // this service (production + any preview-pr-N). The dropdown used
  // to be hard-coded "all envs / production" so users couldn't scope
  // to a preview at all.
  const envs = useEnvironments(project);
  const fqn = service ? project + "-" + service : "";
  const envOptions = useMemo(() => {
    const list = (envs.data ?? []).filter((e) => e.spec.service === fqn);
    return list
      .map((e) => {
        if (e.spec.kind === "production") return { value: "production", label: "production" };
        // Env CR name is `<project>-<service>-<short>`; the last two
        // dash-segments are the short identifier (e.g. "preview-pr-12").
        const short = e.metadata.name.split("-").slice(-2).join("-");
        return { value: short, label: short };
      })
      .sort((a, b) => {
        if (a.value === "production") return -1;
        if (b.value === "production") return 1;
        return a.label.localeCompare(b.label);
      });
  }, [envs.data, fqn]);

  // Convert "1h" → RFC3339 absolute. The server accepts RFC3339 or
  // unix; we send RFC3339 for consistency with the time pickers.
  const sinceISO = useMemo(() => {
    const m = committed.since.match(/^(\d+)([hdm])$/);
    if (!m) return committed.since; // assume already absolute
    const n = parseInt(m[1], 10);
    const unit = m[2];
    const ms = unit === "h" ? n * 3600_000 : unit === "d" ? n * 86_400_000 : n * 60_000;
    return new Date(Date.now() - ms).toISOString();
  }, [committed.since]);

  const search = useQuery<LogSearchResponse>({
    queryKey: ["log-search", project, service, committed.q, committed.env, committed.since],
    queryFn: () =>
      searchServiceLogs(project, service, {
        q: committed.q || undefined,
        env: committed.env || undefined,
        since: sinceISO,
        limit: 200,
      }),
    refetchInterval: committed.q === "" ? 10_000 : false, // live tail when no query
    staleTime: 5_000,
  });

  const apply = () => setCommitted({ q, env, since });

  return (
    <div className="space-y-3 p-5">
      <header className="flex items-center justify-between gap-2">
        <div>
          <h3 className="font-mono text-sm font-medium">Logs</h3>
          <p className="font-mono text-[11px] text-[var(--text-tertiary)]">
            Searchable archive (FTS5). 14d retention. Polls every 10s when no query is set — for true streaming, use the Deployments tab.
          </p>
        </div>
      </header>

      {/* Search bar */}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          apply();
        }}
        className="flex flex-wrap items-center gap-2"
      >
        <div className="relative flex-1 min-w-[200px]">
          <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[var(--text-tertiary)]" />
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder='FTS5 — "fatal error" OR oom*'
            className="h-8 pl-7 font-mono text-[12px]"
          />
          {q && (
            <button
              type="button"
              onClick={() => {
                setQ("");
                setCommitted((c) => ({ ...c, q: "" }));
              }}
              className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-0.5 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
            >
              <X className="h-3 w-3" />
            </button>
          )}
        </div>
        <select
          value={env}
          onChange={(e) => setEnv(e.target.value)}
          className="h-8 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px]"
        >
          <option value="">all envs</option>
          {envOptions.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <div className="inline-flex rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
          {(["10m", "1h", "6h", "24h", "7d"] as const).map((p) => (
            <button
              key={p}
              type="button"
              onClick={() => {
                setSince(p);
                setCommitted((c) => ({ ...c, since: p }));
              }}
              className={cn(
                "rounded px-2 py-1 font-mono text-[10px] transition-colors",
                since === p
                  ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                  : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
              )}
            >
              {p}
            </button>
          ))}
        </div>
        <Button type="submit" size="sm">
          Search
        </Button>
      </form>

      {/* Results */}
      {search.isPending ? (
        <Skeleton className="h-64 w-full" />
      ) : search.isError ? (
        <p className="font-mono text-[11px] text-red-400">
          Failed to load: {search.error instanceof Error ? search.error.message : "unknown"}
        </p>
      ) : (search.data?.lines ?? []).length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-4 py-8 text-center text-[12px] text-[var(--text-tertiary)]">
          {committed.q
            ? `No matches for ${committed.q} in the last ${committed.since}.`
            : `No log lines from this service in the last ${committed.since}.`}
        </p>
      ) : (
        <LogList lines={search.data!.lines} highlight={committed.q} />
      )}
    </div>
  );
}

function LogList({ lines, highlight }: { lines: LogLine[]; highlight: string }) {
  // Reverse — server returns newest-first; humans tail-follow oldest-first.
  const ordered = useMemo(() => [...lines].reverse(), [lines]);

  // Coalesce consecutive lines from the same pod+second so a multi-line
  // stack trace renders as one timestamped block, not 30 lines with the
  // same prefix repeated. The server splits log frames on \n upstream
  // (stream.go:118/143), so a 30-line traceback arrives as 30 frames.
  // Keeping the prefix on each was making errors un-copyable — readers
  // had to manually strip "MM-DD HH:MM:SS  pod  " from every line.
  //
  // Rule: a line is "continuation" of the previous when it has the same
  // pod AND the same fmtTs result (sub-second resolution). The first
  // line of each group shows ts + pod; continuations show empty cells.
  const grouped = useMemo(() => {
    let prevPod = "";
    let prevTs = "";
    return ordered.map((l) => {
      const ts = fmtTs(l.ts);
      const isContinuation = l.pod === prevPod && ts === prevTs;
      prevPod = l.pod;
      prevTs = ts;
      return { line: l, isContinuation };
    });
  }, [ordered]);

  // Auto-scroll to bottom on first paint + when new lines arrive
  // and the user is already near the bottom (don't yank if they're
  // reading old lines).
  const [stickToBottom, setStickToBottom] = useState(true);
  useEffect(() => {
    if (!stickToBottom) return;
    const el = document.getElementById("kuso-log-list");
    if (el) el.scrollTop = el.scrollHeight;
  }, [ordered, stickToBottom]);

  return (
    <div
      id="kuso-log-list"
      onScroll={(e) => {
        const el = e.currentTarget;
        const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        setStickToBottom(nearBottom);
      }}
      className="h-[28rem] overflow-x-hidden overflow-y-auto rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] font-mono text-[11px] leading-snug"
    >
      <table className="w-full table-fixed">
        <tbody>
          {grouped.map(({ line: l, isContinuation }) => (
            <tr key={l.id} className="border-b border-[var(--border-subtle)]/40 last:border-b-0 hover:bg-[var(--bg-tertiary)]/30">
              {/* Timestamp narrower on phones; pod column hidden entirely
                  (176px+128px of fixed cols left the log line ~70px at
                  375px and forced horizontal scroll). table-fixed +
                  break-all lets the message wrap instead of overflowing. */}
              <td className="w-20 align-top px-2 py-1 text-[10px] text-[var(--text-tertiary)] whitespace-nowrap sm:w-44">
                {isContinuation ? "" : fmtTs(l.ts)}
              </td>
              <td className="hidden w-32 align-top px-1 py-1 text-[10px] text-[var(--text-tertiary)] truncate sm:table-cell" title={l.pod}>
                {isContinuation ? "" : shortPod(l.pod)}
              </td>
              <td className="px-2 py-1 break-all text-[var(--text-secondary)]">
                <Highlight text={l.line} query={highlight} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Highlight — wraps every occurrence of `query` (case-insensitive,
// stripped of FTS5 operators) in a <mark>. Cheap implementation;
// real FTS5 highlighting would need the server to return offsets.
function Highlight({ text, query }: { text: string; query: string }) {
  if (!query) return <>{stripAnsi(text)}</>;
  // Strip FTS5 operators + quotes for a naive substring highlight.
  const needle = query
    .replace(/["()*]/g, " ")
    .split(/\s+/)
    .filter((s) => s.length > 1 && !/^(AND|OR|NOT)$/i.test(s));
  if (needle.length === 0) return <>{stripAnsi(text)}</>;
  const re = new RegExp(`(${needle.map(escapeRe).join("|")})`, "ig");
  const stripped = stripAnsi(text);
  const parts = stripped.split(re);
  return (
    <>
      {parts.map((p, i) =>
        re.test(p) ? (
          <mark key={i} className="rounded bg-[var(--accent-subtle)] px-0.5 text-[var(--text-primary)]">
            {p}
          </mark>
        ) : (
          <span key={i}>{p}</span>
        )
      )}
    </>
  );
}

function escapeRe(s: string): string {
  return s.replace(/[-/\\^$*+?.()|[\]{}]/g, "\\$&");
}

// Strip ANSI escape sequences (kaniko / nginx / postgres all emit
// colour codes that the FTS5 store keeps verbatim).
function stripAnsi(s: string): string {
  // eslint-disable-next-line no-control-regex
  return s.replace(/\x1b\[[0-9;]*[A-Za-z]/g, "");
}

function fmtTs(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  const ss = String(d.getSeconds()).padStart(2, "0");
  const mo = String(d.getMonth() + 1).padStart(2, "0");
  const da = String(d.getDate()).padStart(2, "0");
  return `${mo}-${da} ${hh}:${mm}:${ss}`;
}

function shortPod(pod: string): string {
  // Drop the deployment hash suffix so "kuso-hello-go-…-7d8f-abcd" → "…-7d8f-abcd"
  // Keep last two dash-segments which is the rs hash + pod hash.
  const parts = pod.split("-");
  if (parts.length <= 2) return pod;
  return parts.slice(-2).join("-");
}
