"use client";

import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  ArrowDown,
  Clock,
  Copy,
  CornerDownLeft,
  Download,
  History,
  RefreshCw,
  Search,
  WrapText,
  X,
} from "lucide-react";
import { useLogStream, searchArchive } from "@/features/logs";
import type { ArchivePage, StreamConnection } from "@/features/logs";
import { useEnvironments } from "@/features/projects";
import { serviceEnvOptions } from "@/lib/env-group";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { AnsiText } from "./AnsiText";
import { JsonSummary } from "./LogLineText";
import { LogDetailPanel } from "./LogDetailPanel";
import { TimeRange, rangeLabel, rangeToMs } from "./TimeRange";
import { buildMatcher, detectLevel, fmtClock, linesToText, LOG_LEVELS, parseJsonLine, shortPod, stripAnsi } from "./logParse";
import type { LogLevel } from "./logParse";
import {
  getLogView,
  loadRecentQueries,
  logViewKey,
  rememberQuery,
  setLogView,
  subscribeLogView,
  takePendingLogView,
} from "./logViewStore";
import type { LogRange, LogViewState } from "./logViewStore";

// One row in the viewer, from either the live tail or the archive.
export interface ViewLine {
  key: string;
  id?: number; // archive id (absent for live lines)
  ts: string;
  pod: string;
  line: string;
  stream?: string;
  service?: string;
  env?: string;
  // The source object, used as the level-cache key.
  src: object;
}

interface Row extends ViewLine {
  level: LogLevel;
  json: Record<string, unknown> | null;
  // Same pod + same instant as the previous row: a multi-line frame
  // (stack trace) split by the shipper. Inherits the head's level.
  cont: boolean;
}

const ARCHIVE_PAGE = 200;
const LIVE_PRESETS = ["live", "15m", "1h", "6h", "24h", "7d"] as const;
const ARCHIVE_PRESETS = ["15m", "1h", "6h", "24h", "7d"] as const;

// Level detection + JSON parsing is regex work; cache it per line
// object so a live flush only classifies the rows it added.
interface Parsed {
  level: LogLevel;
  json: Record<string, unknown> | null;
  // The app's own timestamp from a JSON line, when it parses.
  appTs?: string;
}
const parseCache = new WeakMap<object, Parsed>();
function parsedOf(obj: object, line: string): Parsed {
  let p = parseCache.get(obj);
  if (!p) {
    const json = parseJsonLine(line);
    const t = json ? (json.time ?? json.timestamp ?? json.ts ?? json["@timestamp"]) : undefined;
    const appTs = typeof t === "string" && !Number.isNaN(Date.parse(t)) ? t : undefined;
    p = { level: detectLevel(line), json, appTs };
    parseCache.set(obj, p);
  }
  return p;
}

const CONT_SHAPE = /^(\s|at |File "|Caused by|\.\.\. \d+ more|goroutine \d+|\}|\])/;

export interface LogViewerProps {
  project: string;
  // Omitted = project-wide archive search across every service.
  service?: string;
  // Env CR name for the live WebSocket tail. Omitted = no live mode.
  streamEnv?: string;
  // Env group (production / staging / preview-pr-N) for archive queries.
  defaultEnv?: string;
  // Fills the parent's height instead of the default viewport-based one.
  className?: string;
  // Label for the service column (project scope).
  serviceLabel?: (fqn: string) => string;
  // Shown in the empty state ("view build logs").
  onOpenDeployments?: () => void;
}

function useLogViewState(key: string, init: () => LogViewState): [LogViewState, (p: Partial<LogViewState>) => void] {
  const initRef = useRef(init);
  initRef.current = init;
  const state = useSyncExternalStore(
    useCallback((fn) => subscribeLogView(key, fn), [key]),
    () => getLogView(key, initRef.current),
    () => getLogView(key, initRef.current),
  );
  const set = useCallback((p: Partial<LogViewState>) => setLogView(key, p, initRef.current), [key]);
  return [state, set];
}

export function LogViewer({
  project,
  service,
  streamEnv,
  defaultEnv = "",
  className,
  serviceLabel,
  onOpenDeployments,
}: LogViewerProps) {
  const canStream = !!service && !!streamEnv;
  const key = logViewKey(project, service, streamEnv ?? defaultEnv);
  const [view, setView] = useLogViewState(key, () => ({
    q: "",
    range: canStream ? { kind: "live" } : { kind: "preset", preset: "1h" },
    env: defaultEnv,
    levels: [],
    pod: "",
    wrap: true,
    scrollTop: null,
    ...takePendingLogView(project, service),
  }));
  // A hand-off from another tab (Errors → View in logs) that arrived
  // after this key was first initialised.
  useEffect(() => {
    const p = takePendingLogView(project, service);
    if (p) setView(p);
  }, [project, service, setView]);

  const live = canStream && view.range.kind === "live";
  const [draft, setDraft] = useState(view.q);
  useEffect(() => setDraft(view.q), [view.q]);
  const [selected, setSelected] = useState<Row | null>(null);

  // ---- live ---------------------------------------------------------------
  const stream = useLogStream(live ? project : "", service ?? "", streamEnv ?? "", 300);

  // ---- archive ------------------------------------------------------------
  const archiveScope = useMemo(() => ({ project, service }), [project, service]);
  const rangeKey = view.range.kind === "abs" ? `${view.range.from}/${view.range.to}` : view.range.kind === "preset" ? view.range.preset : "live";
  const archive = useInfiniteQuery<ArchivePage>({
    queryKey: ["logs-archive", project, service ?? "*", view.q, view.env, rangeKey],
    queryFn: ({ pageParam }) => {
      const r = view.range;
      const since =
        r.kind === "abs" ? r.from : r.kind === "preset" ? new Date(Date.now() - rangeToMs(r.preset)).toISOString() : undefined;
      return searchArchive(archiveScope, {
        q: view.q || undefined,
        env: view.env || undefined,
        since,
        until: r.kind === "abs" ? r.to : undefined,
        limit: ARCHIVE_PAGE,
        beforeId: pageParam as number | undefined,
      });
    },
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => last.nextBeforeId,
    enabled: !live && !!project,
    // Poll only on the newest page of a relative window: an infinite
    // query refetches every loaded page, which would re-download the
    // scrollback and shift it under the reader.
    refetchInterval: (query) =>
      view.range.kind === "preset" && (query.state.data?.pages.length ?? 0) <= 1 ? 10_000 : false,
    refetchOnWindowFocus: (query) => (query.state.data?.pages.length ?? 0) <= 1,
    staleTime: 5_000,
  });

  // ---- rows ---------------------------------------------------------------
  const rawLines: ViewLine[] = useMemo(() => {
    if (live) {
      return stream.lines.map((l) => ({ key: `l${l.id}`, ts: l.ts, pod: l.pod, line: l.line, stream: l.stream, src: l }));
    }
    const out: ViewLine[] = [];
    const pages = archive.data?.pages ?? [];
    // Pages are newest-first; render oldest-first like a terminal.
    for (let p = pages.length - 1; p >= 0; p--) {
      const ls = pages[p].lines;
      for (let i = ls.length - 1; i >= 0; i--) {
        const l = ls[i];
        out.push({ key: `a${l.id}`, id: l.id, ts: l.ts, pod: l.pod, line: l.line, service: l.service, env: l.env, src: l });
      }
    }
    return out;
  }, [live, stream.lines, archive.data]);

  const rows: Row[] = useMemo(() => {
    let prev: Row | null = null;
    return rawLines.map((l) => {
      const p = parsedOf(l.src, l.line);
      // A continuation (stack-trace line) shares its head's pod and
      // instant, carries no level of its own, and either looks like a
      // trace line or follows an error/warn head.
      const sameInstant =
        prev !== null && prev.pod === l.pod && Math.abs(Date.parse(prev.ts) - Date.parse(l.ts)) <= 2;
      const cont =
        sameInstant &&
        p.level === "none" &&
        !p.json &&
        (CONT_SHAPE.test(stripAnsi(l.line)) || prev?.level === "error" || prev?.level === "warn");
      const row: Row = {
        ...l,
        ts: p.appTs ?? l.ts,
        level: cont && prev ? prev.level : p.level,
        cont,
        json: p.json,
      };
      prev = row;
      return row;
    });
  }, [rawLines]);

  const liveMatcher = useMemo(() => (live ? buildMatcher(view.q) : null), [live, view.q]);
  const highlightRe = useMemo(() => buildMatcher(view.q)?.re ?? null, [view.q]);

  const visible = useMemo(() => {
    const lv = new Set(view.levels);
    return rows.filter(
      (r) =>
        (lv.size === 0 || lv.has(r.level)) &&
        (!view.pod || r.pod === view.pod) &&
        (!liveMatcher || liveMatcher.test(r.line)),
    );
  }, [rows, view.levels, view.pod, liveMatcher]);

  const levelCounts = useMemo(() => {
    const c: Record<LogLevel, number> = { error: 0, warn: 0, info: 0, debug: 0, none: 0 };
    for (const r of rows) if (!r.cont) c[r.level]++;
    return c;
  }, [rows]);

  const pods = useMemo(() => [...new Set(rows.map((r) => r.pod))].sort(), [rows]);
  const multiPod = pods.length > 1 || !!view.pod;

  // ---- envs (archive filter) ----------------------------------------------
  const envs = useEnvironments(project);
  const envOptions = useMemo(() => {
    const all = envs.data ?? [];
    if (service) return serviceEnvOptions(all, `${project}-${service}`);
    const seen = new Map<string, string>();
    for (const fqn of new Set(all.map((e) => e.spec.service ?? ""))) {
      for (const o of serviceEnvOptions(all, fqn)) seen.set(o.value, o.label);
    }
    return [...seen.entries()].map(([value, label]) => ({ value, label }));
  }, [envs.data, project, service]);

  // ---- scrolling ----------------------------------------------------------
  const scrollerRef = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(view.scrollTop === null);
  const [unseen, setUnseen] = useState(0);
  const lastCountRef = useRef(0);
  const restoreRef = useRef<number | null>(null);

  // Restore the reader's scroll position from before the tab switch.
  useLayoutEffect(() => {
    const el = scrollerRef.current;
    if (el && view.scrollTop !== null) el.scrollTop = view.scrollTop;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  useEffect(() => {
    const el = scrollerRef.current;
    return () => {
      if (el) setLogView(key, { scrollTop: el.scrollHeight - el.scrollTop - el.clientHeight < 40 ? null : el.scrollTop });
    };
  }, [key]);

  useLayoutEffect(() => {
    const el = scrollerRef.current;
    if (!el) return;
    if (restoreRef.current !== null) {
      el.scrollTop = el.scrollHeight - restoreRef.current;
      restoreRef.current = null;
    } else if (follow) {
      el.scrollTop = el.scrollHeight;
    }
    const added = visible.length - lastCountRef.current;
    if (!follow && added > 0 && live) setUnseen((n) => n + added);
    lastCountRef.current = visible.length;
  }, [visible, follow, live]);

  // Rows use content-visibility, so their real heights land after the
  // first layout; keep pinned to the tail as the content settles.
  const contentRef = useRef<HTMLDivElement>(null);
  const followRef = useRef(follow);
  followRef.current = follow;
  useEffect(() => {
    const el = contentRef.current;
    const sc = scrollerRef.current;
    if (!el || !sc) return;
    const ro = new ResizeObserver(() => {
      if (followRef.current && restoreRef.current === null) sc.scrollTop = sc.scrollHeight;
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const jumpToLatest = () => {
    setFollow(true);
    setUnseen(0);
    const el = scrollerRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  };

  const onScroll = () => {
    const el = scrollerRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    if (atBottom !== follow) {
      setFollow(atBottom);
      if (atBottom) setUnseen(0);
    }
    if (!live && el.scrollTop < 80 && archive.hasNextPage && !archive.isFetchingNextPage) {
      restoreRef.current = el.scrollHeight;
      void archive.fetchNextPage();
    }
  };

  // Re-follow whenever the result set is replaced.
  useEffect(() => {
    setFollow(true);
    setUnseen(0);
    setSelected(null);
  }, [rangeKey, view.q, view.env, live]);

  // ---- actions --------------------------------------------------------------
  const [recent, setRecent] = useState<string[]>([]);
  const [showRecent, setShowRecent] = useState(false);
  useEffect(() => setRecent(loadRecentQueries()), []);

  const commitQuery = (q: string, toArchive: boolean) => {
    const t = q.trim();
    if (t) setRecent(rememberQuery(t));
    const patch: Partial<LogViewState> = { q: t };
    if (toArchive && live) patch.range = { kind: "preset", preset: "24h" };
    setView(patch);
    setShowRecent(false);
  };

  const setRange = (v: string) => {
    const range: LogRange = v === "live" ? { kind: "live" } : { kind: "preset", preset: v };
    setView({ range, scrollTop: null });
  };

  const toggleLevel = (l: LogLevel) => {
    const has = view.levels.includes(l);
    setView({ levels: has ? view.levels.filter((x) => x !== l) : [...view.levels, l] });
  };

  const copyVisible = async () => {
    try {
      await navigator.clipboard.writeText(linesToText(visible, false));
      toast.success(`Copied ${visible.length} line${visible.length === 1 ? "" : "s"}`);
    } catch {
      toast.error("Copy failed — clipboard unavailable");
    }
  };

  const download = () => {
    const text = linesToText(visible, true);
    const blob = new Blob([text + "\n"], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    const stamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
    a.href = url;
    a.download = `${project}${service ? "-" + service : ""}-${stamp}.log`;
    a.click();
    URL.revokeObjectURL(url);
  };

  // Keyboard: ↑/↓ step through rows while the detail panel is open.
  const onListKey = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (!selected || (e.key !== "ArrowDown" && e.key !== "ArrowUp")) return;
    e.preventDefault();
    const i = visible.findIndex((r) => r.key === selected.key);
    const next = visible[i + (e.key === "ArrowDown" ? 1 : -1)];
    if (next) {
      setSelected(next);
      document.getElementById(rowDomId(next.key))?.scrollIntoView({ block: "nearest" });
    }
  };

  // ---- derived labels -----------------------------------------------------
  const presets = canStream ? LIVE_PRESETS : ARCHIVE_PRESETS;
  const rangeValue = view.range.kind === "live" ? "live" : view.range.kind === "preset" ? view.range.preset : "";
  const customLabel =
    view.range.kind === "abs" ? `${fmtClock(view.range.from, true).slice(0, -4)} – ${fmtClock(view.range.to, false).slice(0, -4)}` : undefined;
  const spanMs = view.range.kind === "preset" ? rangeToMs(view.range.preset) : view.range.kind === "abs" ? Date.parse(view.range.to) - Date.parse(view.range.from) : 0;
  const withDate = !live && spanMs > 20 * 3_600_000;
  const filtersActive = view.levels.length > 0 || !!view.pod;
  const loading = live ? stream.connection === "connecting" && rows.length === 0 : archive.isPending;

  return (
    <div className={cn("flex min-h-0 flex-col gap-2", className)}>
      {/* Row 1: search + range */}
      <div className="flex flex-wrap items-center gap-2">
        <form
          role="search"
          className="relative min-w-0 flex-[1_1_16rem]"
          onSubmit={(e) => {
            e.preventDefault();
            commitQuery(draft, true);
          }}
        >
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[var(--text-tertiary)]" />
          <input
            data-primary-search
            value={draft}
            onChange={(e) => {
              setDraft(e.target.value);
              // Live mode filters the buffer as you type.
              if (live) setView({ q: e.target.value });
            }}
            onFocus={() => setShowRecent(true)}
            onBlur={() => setTimeout(() => setShowRecent(false), 150)}
            onKeyDown={(e) => {
              if (e.key === "Escape" && draft) {
                e.preventDefault();
                setDraft("");
                setView({ q: "" });
              }
            }}
            aria-label={live ? "Filter live logs" : "Search logs"}
            placeholder={live ? "Filter live lines — Enter searches history" : "Search logs (substring, case-insensitive)"}
            spellCheck={false}
            autoComplete="off"
            className="h-8 w-full rounded-md border border-[var(--border-default)] bg-[var(--bg-primary)] pl-8 pr-16 font-mono text-[13px] text-[var(--text-primary)] outline-none placeholder:text-[var(--text-tertiary)] focus-visible:border-[var(--border-strong)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]/30"
          />
          <div className="absolute right-1.5 top-1/2 flex -translate-y-1/2 items-center gap-1">
            {draft && (
              <button
                type="button"
                onClick={() => {
                  setDraft("");
                  setView({ q: "" });
                }}
                aria-label="Clear search"
                className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <X className="h-3 w-3" />
              </button>
            )}
            {draft.trim() && (live || draft.trim() !== view.q) && (
              <button
                type="submit"
                title={live ? "Search the 7-day archive" : "Search"}
                className="inline-flex items-center gap-0.5 rounded border border-[var(--border-subtle)] px-1 py-0.5 text-[11px] text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
              >
                <CornerDownLeft className="h-3 w-3" />
                <span className="sr-only">{live ? "Search history" : "Search"}</span>
              </button>
            )}
          </div>
          {showRecent && recent.length > 0 && !draft && (
            <div className="absolute left-0 right-0 top-9 z-20 overflow-hidden rounded-md border border-[var(--border-default)] bg-[var(--bg-elevated)] py-1 shadow-[var(--shadow-md)]">
              <div className="flex items-center gap-1.5 px-2.5 pb-1 pt-0.5 text-xs text-[var(--text-tertiary)]">
                <History className="h-3 w-3" /> Recent searches
              </div>
              {recent.map((r) => (
                <button
                  key={r}
                  type="button"
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => {
                    setDraft(r);
                    commitQuery(r, true);
                  }}
                  className="block w-full truncate px-2.5 py-1 text-left font-mono text-[13px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                >
                  {r}
                </button>
              ))}
            </div>
          )}
        </form>
        <TimeRange
          presets={presets}
          value={rangeValue}
          onChange={setRange}
          labels={{
            live: (
              <span className="inline-flex items-center gap-1.5">
                <span aria-hidden className={cn("h-1.5 w-1.5 rounded-full", live ? "bg-[var(--success)]" : "bg-[var(--text-tertiary)]")} />
                Live
              </span>
            ),
          }}
          customLabel={customLabel}
          onClearCustom={() => setView({ range: canStream ? { kind: "live" } : { kind: "preset", preset: "1h" } })}
          aria-label="Log time range"
        />
      </div>

      {/* Row 2: status + filters + actions */}
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1.5">
        <div className="min-w-0 flex-[1_1_14rem]">
          <StatusLine
          live={live}
          connection={stream.connection}
          error={stream.error}
          onReconnect={stream.reconnect}
          follow={follow}
          count={visible.length}
          total={rows.length}
          rangeText={
            view.range.kind === "preset" ? `last ${rangeLabel(view.range.preset)}` : view.range.kind === "abs" ? "custom window" : ""
          }
          query={view.q}
          refreshing={!live && archive.isFetching && !archive.isFetchingNextPage && !archive.isPending}
          onBackToLive={canStream && !live ? () => setRange("live") : undefined}
        />
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <div role="group" aria-label="Filter by level" className="inline-flex items-center gap-0.5">
            {LOG_LEVELS.map((l) => (
              <LevelChip key={l} level={l} count={levelCounts[l]} on={view.levels.includes(l)} onClick={() => toggleLevel(l)} />
            ))}
          </div>
          {!live && envOptions.length > 1 && (
            <select
              value={view.env}
              onChange={(e) => setView({ env: e.target.value })}
              aria-label="Environment"
              className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-1.5 text-xs text-[var(--text-secondary)]"
            >
              <option value="">All envs</option>
              {envOptions.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
          )}
          {multiPod && (
            <select
              value={view.pod}
              onChange={(e) => setView({ pod: e.target.value })}
              aria-label="Replica"
              className="h-7 max-w-[11rem] rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-1.5 font-mono text-xs text-[var(--text-secondary)]"
            >
              <option value="">All replicas ({pods.length})</option>
              {pods.map((p) => (
                <option key={p} value={p}>
                  {shortPod(p)}
                </option>
              ))}
            </select>
          )}
          <span aria-hidden className="mx-0.5 h-4 w-px bg-[var(--border-subtle)]" />
          <IconToggle label={view.wrap ? "Don't wrap lines" : "Wrap lines"} pressed={view.wrap} onClick={() => setView({ wrap: !view.wrap })}>
            <WrapText className="h-3.5 w-3.5" />
          </IconToggle>
          <IconToggle label={`Copy ${visible.length} lines`} onClick={() => void copyVisible()} disabled={visible.length === 0}>
            <Copy className="h-3.5 w-3.5" />
          </IconToggle>
          <IconToggle label="Download as .log" onClick={download} disabled={visible.length === 0}>
            <Download className="h-3.5 w-3.5" />
          </IconToggle>
        </div>
      </div>

      {/* Body */}
      <div className="relative min-h-[18rem] flex-1 overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]">
        <div
          ref={scrollerRef}
          onScroll={onScroll}
          onKeyDown={onListKey}
          tabIndex={0}
          role="log"
          aria-live="off"
          aria-label="Log lines"
          className="h-full overflow-y-auto overflow-x-auto py-1 font-mono text-[12px] leading-[1.55] outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--ring)]/40"
        >
          <div ref={contentRef}>
          {!live && rows.length > 0 && (
            <div className="px-3 py-1.5 text-center font-sans text-xs text-[var(--text-tertiary)]">
              {archive.isFetchingNextPage ? (
                "Loading older lines…"
              ) : archive.hasNextPage ? (
                <button type="button" className="underline-offset-2 hover:underline" onClick={() => void archive.fetchNextPage()}>
                  Load older lines
                </button>
              ) : (
                `Start of results for the ${view.range.kind === "preset" ? `last ${rangeLabel(view.range.preset)}` : "selected window"}`
              )}
            </div>
          )}
          {loading ? (
            <LoadingRows />
          ) : !live && archive.isError ? (
            <div className="p-4 font-sans">
              <QueryErrorState what="logs" error={archive.error} onRetry={() => void archive.refetch()} />
            </div>
          ) : visible.length === 0 ? (
            <EmptyLogs
              live={live}
              connection={stream.connection}
              notice={stream.notice}
              hasRows={rows.length > 0}
              filtersActive={filtersActive || (live && !!view.q)}
              query={view.q}
              rangeText={view.range.kind === "preset" ? rangeLabel(view.range.preset) : "the selected window"}
              onClearFilters={() => {
                setView({ levels: [], pod: "", ...(live ? { q: "" } : {}) });
                if (live) setDraft("");
              }}
              onClearQuery={() => {
                setDraft("");
                setView({ q: "" });
              }}
              onWiden={(p) => setRange(p)}
              onReconnect={stream.reconnect}
              onOpenDeployments={onOpenDeployments}
              currentPreset={view.range.kind === "preset" ? view.range.preset : ""}
            />
          ) : (
            visible.map((r) => (
              <LogRow
                key={r.key}
                row={r}
                wrap={view.wrap}
                showPod={multiPod}
                withDate={withDate}
                serviceLabel={serviceLabel}
                highlight={highlightRe}
                selected={selected?.key === r.key}
                onSelect={setSelected}
              />
            ))
          )}
          </div>
        </div>

        {!follow && visible.length > 0 && (
          <button
            type="button"
            onClick={jumpToLatest}
            className="absolute bottom-3 left-1/2 z-10 inline-flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-[var(--border-default)] bg-[var(--bg-elevated)] px-3 py-1 text-xs text-[var(--text-primary)] shadow-[var(--shadow-md)] hover:bg-[var(--bg-tertiary)]"
          >
            <ArrowDown className="h-3 w-3" />
            {live ? (unseen > 0 ? `Paused · ${unseen} new · Jump to live` : "Paused · Jump to live") : "Jump to latest"}
          </button>
        )}

        {selected && (
          <LogDetailPanel
            line={selected}
            level={selected.level}
            scope={archiveScope}
            env={view.env || undefined}
            serviceLabel={serviceLabel}
            onClose={() => {
              setSelected(null);
              scrollerRef.current?.focus();
            }}
          />
        )}
      </div>
    </div>
  );
}

export function rowDomId(key: string): string {
  return `logrow-${key}`;
}

const LEVEL_META: Record<LogLevel, { label: string; bar: string; text: string; row?: string }> = {
  error: { label: "ERR", bar: "bg-[var(--error)]", text: "text-[var(--error)]", row: "bg-[var(--error-subtle)]" },
  warn: { label: "WRN", bar: "bg-[var(--warning)]", text: "text-[var(--warning)]", row: "bg-[var(--warning-subtle)]/60" },
  info: { label: "INF", bar: "bg-[var(--info)]/60", text: "text-[var(--text-tertiary)]" },
  debug: { label: "DBG", bar: "bg-[var(--border-default)]", text: "text-[var(--text-tertiary)]" },
  none: { label: "", bar: "bg-transparent", text: "text-[var(--text-tertiary)]" },
};

const LogRow = memo(function LogRow({
  row: r,
  wrap,
  showPod,
  withDate,
  serviceLabel,
  highlight,
  selected,
  onSelect,
}: {
  row: Row;
  wrap: boolean;
  showPod: boolean;
  withDate: boolean;
  serviceLabel?: (fqn: string) => string;
  highlight: RegExp | null;
  selected: boolean;
  onSelect: (r: Row) => void;
}) {
  const meta = LEVEL_META[r.level];
  return (
    <div
      id={rowDomId(r.key)}
      onClick={() => {
        // Let a drag-select copy text without opening the panel.
        if (window.getSelection()?.toString()) return;
        onSelect(r);
      }}
      className={cn(
        "group relative flex cursor-pointer gap-x-3 pl-3 pr-3 max-sm:flex-wrap [contain-intrinsic-size:auto_1.55em] [content-visibility:auto] hover:bg-[var(--bg-tertiary)]/60",
        !r.cont && "mt-px",
        meta.row,
        selected && "bg-[var(--bg-tertiary)] outline outline-1 -outline-offset-1 outline-[var(--border-default)] hover:bg-[var(--bg-tertiary)]",
      )}
    >
      <span aria-hidden className={cn("absolute bottom-0 left-0 top-0 w-[3px]", meta.bar, r.cont && "opacity-50")} />
      <span
        className={cn("shrink-0 select-none tabular-nums text-[var(--text-tertiary)]", withDate ? "w-[10.5rem]" : "w-[6.5rem]", r.cont && "max-sm:hidden")}
        title={r.ts}
      >
        {r.cont ? "" : withDate ? fmtClock(r.ts, true) : <ClockCell ts={r.ts} />}
      </span>
      <span className={cn("w-7 shrink-0 select-none text-[11px] font-semibold leading-[1.85]", meta.text, r.cont && "max-sm:hidden")} aria-label={r.level === "none" ? undefined : `level ${r.level}`}>
        {r.cont ? "" : meta.label}
      </span>
      {serviceLabel && r.service !== undefined && (
        <span className="w-24 shrink-0 select-none truncate text-[var(--text-secondary)]" title={r.service}>
          {r.cont ? "" : serviceLabel(r.service)}
        </span>
      )}
      {showPod && (
        <span className="hidden w-[7.5rem] shrink-0 select-none truncate text-[var(--text-tertiary)] sm:inline" title={r.pod}>
          {r.cont ? "" : shortPod(r.pod)}
        </span>
      )}
      <span
        className={cn(
          "min-w-0 flex-1 text-[var(--text-primary)] max-sm:basis-full",
          wrap ? "whitespace-pre-wrap break-words [overflow-wrap:anywhere]" : "whitespace-pre max-sm:whitespace-pre-wrap max-sm:[overflow-wrap:anywhere]",
          r.level === "debug" && "text-[var(--text-secondary)]",
        )}
      >
        {r.json ? <JsonSummary json={r.json} highlight={highlight} /> : <AnsiText text={r.line} highlight={highlight} />}
      </span>
    </div>
  );
});

// HH:MM:SS with the milliseconds de-emphasised.
function ClockCell({ ts }: { ts: string }) {
  const s = fmtClock(ts, false);
  return (
    <>
      {s.slice(0, 8)}
      <span className="opacity-60">{s.slice(8)}</span>
    </>
  );
}

function LevelChip({ level, count, on, onClick }: { level: Exclude<LogLevel, "none">; count: number; on: boolean; onClick: () => void }) {
  const names = { error: "Error", warn: "Warn", info: "Info", debug: "Debug" } as const;
  const dot = { error: "bg-[var(--error)]", warn: "bg-[var(--warning)]", info: "bg-[var(--info)]", debug: "bg-[var(--text-tertiary)]" }[level];
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={onClick}
      title={`${on ? "Hide" : "Show only"} ${names[level].toLowerCase()} lines`}
      className={cn(
        "inline-flex h-7 items-center gap-1.5 rounded-md border px-2 text-xs transition-colors",
        on
          ? "border-[var(--border-default)] bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
          : "border-transparent text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]",
      )}
    >
      <span aria-hidden className={cn("h-1.5 w-1.5 rounded-full", dot)} />
      {names[level]}
      <span className="tabular-nums text-[var(--text-tertiary)]">{count}</span>
    </button>
  );
}

function IconToggle({
  label,
  pressed,
  onClick,
  disabled,
  children,
}: {
  label: string;
  pressed?: boolean;
  onClick: () => void;
  disabled?: boolean;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      aria-pressed={pressed}
      onClick={onClick}
      disabled={disabled}
      className={cn(
        "inline-flex h-7 w-7 items-center justify-center rounded-md border transition-colors disabled:opacity-40",
        pressed
          ? "border-[var(--border-default)] bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
          : "border-transparent text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]",
      )}
    >
      {children}
    </button>
  );
}

function StatusLine({
  live,
  connection,
  error,
  onReconnect,
  follow,
  count,
  total,
  rangeText,
  query,
  refreshing,
  onBackToLive,
}: {
  live: boolean;
  connection: StreamConnection;
  error: string | null;
  onReconnect: () => void;
  follow: boolean;
  count: number;
  total: number;
  rangeText: string;
  query: string;
  refreshing: boolean;
  onBackToLive?: () => void;
}) {
  const shown = count === total ? `${total.toLocaleString()} lines` : `${count.toLocaleString()} of ${total.toLocaleString()} lines`;
  if (live) {
    const state =
      connection === "live"
        ? follow
          ? { dot: "bg-[var(--success)] animate-pulse motion-reduce:animate-none", text: "Live" }
          : { dot: "bg-[var(--building)]", text: "Paused" }
        : connection === "connecting"
          ? { dot: "bg-[var(--text-tertiary)] animate-pulse motion-reduce:animate-none", text: "Connecting…" }
          : connection === "reconnecting"
            ? { dot: "bg-[var(--building)] animate-pulse motion-reduce:animate-none", text: "Reconnecting…" }
            : connection === "ended"
              ? { dot: "bg-[var(--text-tertiary)]", text: "Stream ended" }
              : { dot: "bg-[var(--error)]", text: "Disconnected" };
    return (
      <div className="flex min-w-0 items-center gap-2 text-xs text-[var(--text-secondary)]" aria-live="polite">
        <span aria-hidden className={cn("h-2 w-2 shrink-0 rounded-full", state.dot)} />
        <span className="font-medium text-[var(--text-primary)]">{state.text}</span>
        <span className="text-[var(--text-tertiary)]">· {shown}</span>
        {(connection === "reconnecting" || connection === "ended" || connection === "failed") && (
          <button type="button" onClick={onReconnect} className="inline-flex items-center gap-1 rounded px-1 text-[var(--text-primary)] underline-offset-2 hover:underline">
            <RefreshCw className="h-3 w-3" />
            {connection === "reconnecting" ? "Retry now" : "Reconnect"}
          </button>
        )}
        {connection === "failed" && error && <span className="truncate text-[var(--error)]">{error}</span>}
      </div>
    );
  }
  return (
    <div className="flex min-w-0 items-center gap-2 whitespace-nowrap text-xs text-[var(--text-secondary)]">
      <Clock aria-hidden className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
      <span className="font-medium text-[var(--text-primary)]">History</span>
      <span className="min-w-0 truncate text-[var(--text-tertiary)]" title={query ? `${rangeText} · matching ${query}` : rangeText}>
        · {rangeText}
        {query ? (
          <>
            {" "}· “<span className="font-mono text-[var(--text-secondary)]">{query}</span>”
          </>
        ) : null}
      </span>
      <span className="shrink-0 text-[var(--text-tertiary)]">· {shown}</span>
      {refreshing && <RefreshCw aria-label="Refreshing" className="h-3 w-3 shrink-0 animate-spin text-[var(--text-tertiary)] motion-reduce:animate-none" />}
      {onBackToLive && (
        <button type="button" onClick={onBackToLive} className="inline-flex shrink-0 items-center gap-1 rounded px-1 text-[var(--text-primary)] underline-offset-2 hover:underline">
          <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-[var(--success)]" />
          Back to live
        </button>
      )}
    </div>
  );
}

function LoadingRows() {
  return (
    <div className="space-y-1.5 px-3 py-2" aria-label="Loading logs">
      {[72, 54, 88, 40, 66, 80, 48].map((w, i) => (
        <div key={i} className="flex gap-3">
          <div className="h-3.5 w-[6.5rem] animate-pulse rounded bg-[var(--bg-tertiary)] motion-reduce:animate-none" />
          <div className="h-3.5 animate-pulse rounded bg-[var(--bg-tertiary)] motion-reduce:animate-none" style={{ width: `${w}%` }} />
        </div>
      ))}
    </div>
  );
}

function EmptyLogs({
  live,
  connection,
  notice,
  hasRows,
  filtersActive,
  query,
  rangeText,
  currentPreset,
  onClearFilters,
  onClearQuery,
  onWiden,
  onReconnect,
  onOpenDeployments,
}: {
  live: boolean;
  connection: StreamConnection;
  notice: string | null;
  hasRows: boolean;
  filtersActive: boolean;
  query: string;
  rangeText: string;
  currentPreset: string;
  onClearFilters: () => void;
  onClearQuery: () => void;
  onWiden: (preset: string) => void;
  onReconnect: () => void;
  onOpenDeployments?: () => void;
}) {
  let title: string;
  let body: React.ReactNode;
  const actions: React.ReactNode[] = [];

  if (hasRows && filtersActive) {
    title = "No lines match these filters";
    body = "Lines are loaded, but none match the level, replica or text filter.";
    actions.push(
      <Button key="clear" size="sm" variant="outline" onClick={onClearFilters}>
        Clear filters
      </Button>,
    );
  } else if (live) {
    if (notice) {
      title = "Nothing to stream";
      body = notice;
    } else if (connection === "failed" || connection === "ended") {
      title = connection === "failed" ? "Couldn't connect to the log stream" : "The log stream ended";
      body = "The service may have been stopped or redeployed.";
      actions.push(
        <Button key="re" size="sm" variant="outline" onClick={onReconnect}>
          <RefreshCw className="h-3.5 w-3.5" /> Reconnect
        </Button>,
      );
    } else {
      title = "Waiting for new log lines";
      body = "Connected. Nothing has been logged since the stream opened. Older lines are in the history.";
    }
    actions.push(
      <Button key="h" size="sm" variant="outline" onClick={() => onWiden("24h")}>
        <Clock className="h-3.5 w-3.5" /> Show last 24 hours
      </Button>,
    );
  } else {
    title = query ? `No matches for “${query}” in the last ${rangeText}` : `No logs in the last ${rangeText}`;
    body = query
      ? "Search is a case-insensitive substring match over the 7-day archive."
      : "The service may be sleeping, stopped, or not deployed yet — or it simply hasn't logged anything.";
    const wider = ["24h", "7d"].filter((p) => rangeToMs(p) > rangeToMs(currentPreset));
    for (const p of wider) {
      actions.push(
        <Button key={p} size="sm" variant="outline" onClick={() => onWiden(p)}>
          Widen to {rangeLabel(p)}
        </Button>,
      );
    }
    if (query) {
      actions.push(
        <Button key="cq" size="sm" variant="ghost" onClick={onClearQuery}>
          Clear search
        </Button>,
      );
    }
  }
  if (onOpenDeployments && !hasRows) {
    actions.push(
      <Button key="dep" size="sm" variant="ghost" onClick={onOpenDeployments}>
        View build logs
      </Button>,
    );
  }
  return (
    <div className="flex min-h-[16rem] flex-col items-center justify-center gap-2 px-6 py-10 text-center font-sans">
      <p className="text-sm font-medium text-[var(--text-primary)]">{title}</p>
      <p className="max-w-md text-[13px] text-[var(--text-secondary)]">{body}</p>
      {actions.length > 0 && <div className="mt-2 flex flex-wrap justify-center gap-2">{actions}</div>}
    </div>
  );
}
