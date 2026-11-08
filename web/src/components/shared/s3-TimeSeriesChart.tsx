"use client";

import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, PointerEvent } from "react";
import { cn } from "@/lib/utils";

// A small hand-rolled time-series chart (inline SVG, no chart lib):
// one series, hairline grid, y ticks on clean numbers, time ticks on
// clean intervals, hover/keyboard crosshair with a tooltip, optional
// threshold lines (limits) and event markers (deploys). A series with
// too few samples for a trend draws as dots (seriesShape). Several charts
// can share one crosshair through hoverT/onHoverT.

export type Point = [number, number | null]; // [epoch ms, value]

export interface ChartMarker {
  t: number;
  label: string;
  detail?: string;
}

export interface ChartThreshold {
  value: number;
  label: string;
}

export interface TimeSeriesChartProps {
  points: Point[];
  domain: [number, number];
  format: (v: number) => string;
  ariaLabel: string;
  height?: number;
  // Lines break across gaps wider than this (missing scrapes / no traffic).
  gapMs?: number;
  thresholds?: ChartThreshold[];
  markers?: ChartMarker[];
  // Minimum y-axis top so a flat-zero series doesn't fill the plot.
  minYMax?: number;
  hoverT?: number | null;
  onHoverT?: (t: number | null) => void;
  // Previous render held at reduced opacity while refetching.
  dim?: boolean;
  color?: string;
  // Shade under the line. Off for levels like latency, where the area
  // implies a quantity and its edges at every gap read as drops to zero.
  fill?: boolean;
  // Ticks land on round multiples of this unit (e.g. 1 MiB for bytes).
  tickUnit?: number;
  // Click / Enter on a time: the caller opens something scoped to it
  // (the metrics tab opens Logs around that moment).
  onPick?: (t: number) => void;
  pickHint?: string;
}

const M = { top: 10, right: 10, bottom: 22 };

// yAxisWidth sizes the left gutter to the widest tick label (11px
// tabular digits ≈ 6.4px each) so "512 MiB" or "0.15/s" never clips.
export function yAxisWidth(labels: string[]): number {
  const longest = labels.reduce((m, l) => Math.max(m, l.length), 0);
  return Math.min(84, Math.max(30, Math.ceil(longest * 6.4) + 12));
}

export function TimeSeriesChart({
  points,
  domain,
  format,
  ariaLabel,
  height = 128,
  gapMs,
  thresholds = [],
  markers = [],
  minYMax = 0,
  hoverT,
  onHoverT,
  dim,
  color = "var(--chart-1)",
  fill = true,
  tickUnit = 1,
  onPick,
  pickHint,
}: TimeSeriesChartProps) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [localHover, setLocalHover] = useState<number | null>(null);
  // This chart (not a synced sibling) has the pointer or focus.
  const [owner, setOwner] = useState(false);
  const clipId = useId();

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => setWidth(Math.floor(entries[0].contentRect.width)));
    ro.observe(el);
    setWidth(Math.floor(el.getBoundingClientRect().width));
    return () => ro.disconnect();
  }, []);

  const [t0, t1] = domain;
  const plotH = height - M.top - M.bottom;
  const valid = useMemo(() => points.filter((p): p is [number, number] => p[1] !== null && Number.isFinite(p[1])), [points]);

  const yMax = useMemo(() => {
    let mx = minYMax;
    for (const p of valid) mx = Math.max(mx, p[1]);
    for (const th of thresholds) mx = Math.max(mx, th.value);
    return mx > 0 ? mx : 1;
  }, [valid, thresholds, minYMax]);
  const yTicks = useMemo(() => niceTicks(yMax / tickUnit, 4).map((v) => v * tickUnit), [yMax, tickUnit]);
  const yTop = yTicks[yTicks.length - 1];
  const left = useMemo(() => yAxisWidth(yTicks.map((v) => format(v))), [yTicks, format]);
  const plotW = Math.max(0, width - left - M.right);

  const x = (t: number) => left + ((t - t0) / Math.max(1, t1 - t0)) * plotW;
  const y = (v: number) => M.top + plotH - (v / yTop) * plotH;

  const shape = useMemo(() => seriesShape(points, gapMs, [t0, t1]), [points, gapMs, t0, t1]);

  const xTicks = useMemo(() => timeTicks(t0, t1, Math.max(3, Math.floor(plotW / 80))), [t0, t1, plotW]);

  const activeT = hoverT !== undefined && onHoverT ? hoverT : localHover;
  const setHover = (t: number | null) => (onHoverT ? onHoverT(t) : setLocalHover(t));
  const nearest = useMemo(() => {
    if (activeT === null || valid.length === 0) return null;
    let best = valid[0];
    for (const p of valid) if (Math.abs(p[0] - activeT) < Math.abs(best[0] - activeT)) best = p;
    // Don't snap to a point far from the pointer (e.g. across a gap).
    const tol = gapMs ?? (t1 - t0) / 20;
    return Math.abs(best[0] - activeT) <= tol ? best : null;
  }, [activeT, valid, gapMs, t0, t1]);
  const nearMarker = useMemo(() => {
    if (activeT === null || plotW === 0) return null;
    const pxPerMs = plotW / Math.max(1, t1 - t0);
    return markers.find((m) => Math.abs(m.t - activeT) * pxPerMs <= 6) ?? null;
  }, [activeT, markers, plotW, t0, t1]);

  const onMove = (e: PointerEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - rect.left;
    if (px < left || px > left + plotW) return setHover(null);
    setOwner(true);
    setHover(t0 + ((px - left) / plotW) * (t1 - t0));
  };

  const onKey = (e: KeyboardEvent<SVGSVGElement>) => {
    if (valid.length === 0) return;
    if (e.key === "Escape") return setHover(null);
    if (e.key === "Enter" && onPick && nearest) {
      e.preventDefault();
      return onPick(nearest[0]);
    }
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    const idx = nearest ? valid.indexOf(nearest) : e.key === "ArrowLeft" ? valid.length : -1;
    const next = valid[Math.min(valid.length - 1, Math.max(0, idx + (e.key === "ArrowRight" ? 1 : -1)))];
    setHover(next[0]);
  };

  const line = (seg: [number, number][]) => seg.map((p, i) => `${i ? "L" : "M"}${x(p[0]).toFixed(1)},${y(p[1]).toFixed(1)}`).join("");
  const area = (seg: [number, number][]) =>
    `${line(seg)}L${x(seg[seg.length - 1][0]).toFixed(1)},${y(0)}L${x(seg[0][0]).toFixed(1)},${y(0)}Z`;

  const hairT = nearest ? nearest[0] : activeT !== null && activeT >= t0 && activeT <= t1 ? activeT : null;
  // The pointer's own chart always explains itself, even over a gap;
  // synced siblings only show a readout when they have a value there.
  const showTip = nearest !== null || nearMarker !== null || (owner && hairT !== null);
  const tipLeft = nearest ? x(nearest[0]) : nearMarker ? x(nearMarker.t) : hairT !== null ? x(hairT) : null;

  return (
    <div ref={wrapRef} className={cn("relative w-full select-none transition-opacity", dim && "opacity-60")} style={{ height }}>
      {width > 0 && (
        <svg
          width={width}
          height={height}
          role="img"
          aria-label={ariaLabel}
          tabIndex={0}
          onPointerMove={onMove}
          onPointerLeave={() => {
            setOwner(false);
            setHover(null);
          }}
          onClick={(e) => {
            if (!onPick) return;
            const rect = e.currentTarget.getBoundingClientRect();
            const px = e.clientX - rect.left;
            if (px < left || px > left + plotW) return;
            onPick(nearest ? nearest[0] : t0 + ((px - left) / plotW) * (t1 - t0));
          }}
          onKeyDown={onKey}
          onFocus={() => setOwner(true)}
          onBlur={() => {
            setOwner(false);
            setHover(null);
          }}
          className={cn(
            "block touch-pan-y outline-none focus-visible:rounded focus-visible:ring-2 focus-visible:ring-[var(--ring)]/40",
            onPick && "cursor-pointer",
          )}
        >
          <defs>
            <clipPath id={clipId}>
              <rect x={left} y={M.top - 2} width={plotW} height={plotH + 4} />
            </clipPath>
          </defs>
          {/* grid + y labels */}
          {yTicks.map((v) => (
            <g key={v}>
              <line x1={left} x2={left + plotW} y1={y(v)} y2={y(v)} stroke="var(--border-subtle)" strokeWidth={1} shapeRendering="crispEdges" />
              <text x={left - 8} y={y(v)} dy="0.32em" textAnchor="end" className="fill-[var(--text-tertiary)] text-[11px] tabular-nums">
                {format(v)}
              </text>
            </g>
          ))}
          {/* x labels */}
          {xTicks.map((t) => (
            <text key={t} x={x(t)} y={height - 6} textAnchor="middle" className="fill-[var(--text-tertiary)] text-[11px] tabular-nums">
              {fmtTick(t, t1 - t0)}
            </text>
          ))}
          <g clipPath={`url(#${clipId})`}>
            {shape.sparse ? (
              // Too few samples for a trend: each one is a dot, joined
              // faintly only to an adjacent sample, never filled to zero.
              <g>
                {shape.segments.map((seg, i) =>
                  seg.length > 1 ? (
                    <path key={`l${i}`} d={line(seg)} fill="none" stroke={color} strokeWidth={1.25} opacity={0.45} strokeLinejoin="round" />
                  ) : null,
                )}
                {shape.segments.flat().map((p) => (
                  <circle key={p[0]} cx={x(p[0])} cy={y(p[1])} r={3} fill={color} />
                ))}
              </g>
            ) : (
              <g>
                {shape.segments.map((seg, i) =>
                  fill && seg.length >= DENSE_RUN ? <path key={`a${i}`} d={area(seg)} fill={color} opacity={0.1} /> : null,
                )}
                {shape.segments.map((seg, i) =>
                  seg.length > 1 ? (
                    <path key={`l${i}`} d={line(seg)} fill="none" stroke={color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
                  ) : (
                    <circle key={`l${i}`} cx={x(seg[0][0])} cy={y(seg[0][1])} r={2.5} fill={color} />
                  ),
                )}
              </g>
            )}
            {thresholds.map((th) => (
              <g key={th.label}>
                <line
                  x1={left}
                  x2={left + plotW}
                  y1={y(th.value)}
                  y2={y(th.value)}
                  stroke="var(--text-tertiary)"
                  strokeWidth={1}
                  strokeDasharray="4 3"
                  shapeRendering="crispEdges"
                />
                <text x={left + plotW - 4} y={y(th.value) - 4} textAnchor="end" className="fill-[var(--text-secondary)] text-[11px]">
                  {th.label}
                </text>
              </g>
            ))}
            {markers
              .filter((m) => m.t >= t0 && m.t <= t1)
              .map((m) => (
                <g key={`${m.t}-${m.label}`}>
                  <line x1={x(m.t)} x2={x(m.t)} y1={M.top + 4} y2={M.top + plotH} stroke="var(--text-tertiary)" strokeWidth={1} strokeDasharray="3 3" shapeRendering="crispEdges" />
                  <path d={`M${x(m.t)},${M.top - 2} l5,5 l-5,5 l-5,-5 z`} fill="var(--text-primary)" stroke="var(--bg-secondary)" strokeWidth={1.5} />
                </g>
              ))}
          </g>
          {hairT !== null && (
            <g pointerEvents="none">
              <line
                x1={x(hairT)}
                x2={x(hairT)}
                y1={M.top}
                y2={M.top + plotH}
                stroke="var(--text-secondary)"
                strokeWidth={1}
                opacity={nearest ? 1 : 0.5}
                shapeRendering="crispEdges"
              />
              {nearest && <circle cx={x(nearest[0])} cy={y(nearest[1])} r={4} fill={color} stroke="var(--bg-secondary)" strokeWidth={2} />}
            </g>
          )}
        </svg>
      )}
      {tipLeft !== null && showTip && (
        <div
          role="status"
          className="pointer-events-none absolute top-0 z-10 min-w-[8rem] max-w-[16rem] rounded-md border border-[var(--border-default)] bg-[var(--bg-elevated)] px-2.5 py-1.5 text-xs shadow-[var(--shadow-md)]"
          style={tipLeft > width / 2 ? { right: width - tipLeft + 10 } : { left: tipLeft + 10 }}
        >
          {nearest ? (
            <>
              <div className="font-semibold tabular-nums text-[var(--text-primary)]">{format(nearest[1])}</div>
              <div className="tabular-nums text-[var(--text-tertiary)]">{fmtFull(nearest[0])}</div>
            </>
          ) : (
            !nearMarker &&
            hairT !== null && (
              <>
                <div className="text-[var(--text-secondary)]">No data</div>
                <div className="tabular-nums text-[var(--text-tertiary)]">{fmtFull(hairT)}</div>
              </>
            )
          )}
          {nearMarker && (
            <div className={cn("text-[var(--text-secondary)]", nearest && "mt-1 border-t border-[var(--border-subtle)] pt-1")}>
              <div className="font-medium text-[var(--text-primary)]">◆ {nearMarker.label}</div>
              {nearMarker.detail && <div className="line-clamp-2">{nearMarker.detail}</div>}
              {!nearest && <div className="tabular-nums text-[var(--text-tertiary)]">{fmtFull(nearMarker.t)}</div>}
            </div>
          )}
          {onPick && pickHint && owner && <div className="mt-1 border-t border-[var(--border-subtle)] pt-1 text-[var(--text-tertiary)]">{pickHint}</div>}
        </div>
      )}
    </div>
  );
}

// A run shorter than this draws as a sliver (a two-point line with its
// area under it reads as a spike to zero), so it doesn't count as "line".
export const DENSE_RUN = 3;

export interface SeriesShape {
  // Runs of finite points; a null value or a gap wider than gapMs
  // between neighbours ends a run, so missing data is never drawn as 0.
  segments: [number, number][][];
  samples: number;
  // Too few samples, most of them isolated, or covering under half the
  // window (a few short plateaus look like bars): draw dots, not a line.
  sparse: boolean;
}

export function seriesShape(points: Point[], gapMs?: number, domain?: [number, number]): SeriesShape {
  const segments: [number, number][][] = [];
  let cur: [number, number][] = [];
  let prevT: number | null = null;
  for (const [t, v] of points) {
    const ok = v !== null && Number.isFinite(v);
    if (!ok || (gapMs && prevT !== null && t - prevT > gapMs)) {
      if (cur.length) segments.push(cur);
      cur = [];
    }
    if (ok) {
      cur.push([t, v]);
      prevT = t;
    } else prevT = null;
  }
  if (cur.length) segments.push(cur);
  const samples = segments.reduce((n, s) => n + s.length, 0);
  const inRuns = segments.reduce((n, s) => n + (s.length >= DENSE_RUN ? s.length : 0), 0);
  let sparse = samples > 0 && (samples < 4 || inRuns / samples < 0.6);
  if (!sparse && domain && domain[1] > domain[0]) {
    // Each sample stands for one sampling interval (the median spacing).
    const diffs = segments.flatMap((seg) => seg.slice(1).map((p, i) => p[0] - seg[i][0])).sort((a, b) => a - b);
    const interval = diffs[Math.floor(diffs.length / 2)] ?? 0;
    sparse = (samples * interval) / (domain[1] - domain[0]) < 0.5;
  }
  return { segments, samples, sparse };
}

// niceTicks returns 0..top on a 1/2/2.5/5×10^n step, about `count` ticks.
export function niceTicks(max: number, count: number): number[] {
  if (!(max > 0)) return [0, 1];
  const raw = max / count;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? 10 * mag;
  const out: number[] = [];
  for (let v = 0; v < max + step * 0.999; v += step) out.push(Number(v.toPrecision(12)));
  if (out.length < 2) out.push(step);
  return out;
}

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;
const STEPS = [MIN, 2 * MIN, 5 * MIN, 10 * MIN, 15 * MIN, 30 * MIN, HOUR, 2 * HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR, DAY, 2 * DAY, 7 * DAY];

// timeTicks picks a clean interval and aligns ticks to it in local time.
export function timeTicks(t0: number, t1: number, maxTicks: number): number[] {
  const span = t1 - t0;
  if (span <= 0) return [];
  const step = STEPS.find((s) => span / s <= maxTicks) ?? 7 * DAY;
  const tz = new Date(t0).getTimezoneOffset() * MIN;
  let t = Math.ceil((t0 - tz) / step) * step + tz;
  const out: number[] = [];
  for (; t <= t1; t += step) out.push(t);
  return out;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
function pad(n: number) {
  return String(n).padStart(2, "0");
}
function fmtTick(t: number, span: number): string {
  const d = new Date(t);
  if (span > 2 * DAY) return `${MONTHS[d.getMonth()]} ${d.getDate()}`;
  if (d.getHours() === 0 && d.getMinutes() === 0 && span > 12 * HOUR) return `${MONTHS[d.getMonth()]} ${d.getDate()}`;
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
export function fmtFull(t: number): string {
  const d = new Date(t);
  return `${MONTHS[d.getMonth()]} ${d.getDate()}, ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}
