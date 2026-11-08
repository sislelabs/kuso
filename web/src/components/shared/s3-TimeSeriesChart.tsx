"use client";

import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, PointerEvent } from "react";
import { cn } from "@/lib/utils";

// A small hand-rolled time-series chart (inline SVG, no chart lib):
// one series, hairline grid, y ticks on clean numbers, time ticks on
// clean intervals, hover/keyboard crosshair with a tooltip, optional
// threshold lines (limits) and event markers (deploys). Several charts
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
  // Ticks land on round multiples of this unit (e.g. 1 MiB for bytes).
  tickUnit?: number;
}

const M = { top: 10, right: 10, bottom: 22, left: 46 };

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
  tickUnit = 1,
}: TimeSeriesChartProps) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [localHover, setLocalHover] = useState<number | null>(null);
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
  const plotW = Math.max(0, width - M.left - M.right);
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

  const x = (t: number) => M.left + ((t - t0) / Math.max(1, t1 - t0)) * plotW;
  const y = (v: number) => M.top + plotH - (v / yTop) * plotH;

  const segments = useMemo(() => {
    const segs: [number, number][][] = [];
    let cur: [number, number][] = [];
    let prevT: number | null = null;
    for (const p of points) {
      const v = p[1];
      if (v === null || !Number.isFinite(v) || (gapMs && prevT !== null && p[0] - prevT > gapMs)) {
        if (cur.length) segs.push(cur);
        cur = [];
      }
      if (v !== null && Number.isFinite(v)) cur.push([p[0], v]);
      prevT = p[0];
    }
    if (cur.length) segs.push(cur);
    return segs;
  }, [points, gapMs]);

  const xTicks = useMemo(() => timeTicks(t0, t1, Math.max(2, Math.floor(plotW / 90))), [t0, t1, plotW]);

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
    if (px < M.left || px > M.left + plotW) return setHover(null);
    setHover(t0 + ((px - M.left) / plotW) * (t1 - t0));
  };

  const onKey = (e: KeyboardEvent<SVGSVGElement>) => {
    if (valid.length === 0) return;
    if (e.key === "Escape") return setHover(null);
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    const idx = nearest ? valid.indexOf(nearest) : e.key === "ArrowLeft" ? valid.length : -1;
    const next = valid[Math.min(valid.length - 1, Math.max(0, idx + (e.key === "ArrowRight" ? 1 : -1)))];
    setHover(next[0]);
  };

  const line = (seg: [number, number][]) => seg.map((p, i) => `${i ? "L" : "M"}${x(p[0]).toFixed(1)},${y(p[1]).toFixed(1)}`).join("");
  const area = (seg: [number, number][]) =>
    `${line(seg)}L${x(seg[seg.length - 1][0]).toFixed(1)},${y(0)}L${x(seg[0][0]).toFixed(1)},${y(0)}Z`;

  const tipLeft = nearest ? x(nearest[0]) : nearMarker ? x(nearMarker.t) : null;

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
          onPointerLeave={() => setHover(null)}
          onKeyDown={onKey}
          onBlur={() => setHover(null)}
          className="block touch-pan-y outline-none focus-visible:rounded focus-visible:ring-2 focus-visible:ring-[var(--ring)]/40"
        >
          <defs>
            <clipPath id={clipId}>
              <rect x={M.left} y={M.top - 2} width={plotW} height={plotH + 4} />
            </clipPath>
          </defs>
          {/* grid + y labels */}
          {yTicks.map((v) => (
            <g key={v}>
              <line x1={M.left} x2={M.left + plotW} y1={y(v)} y2={y(v)} stroke="var(--border-subtle)" strokeWidth={1} shapeRendering="crispEdges" />
              <text x={M.left - 8} y={y(v)} dy="0.32em" textAnchor="end" className="fill-[var(--text-tertiary)] text-[11px] tabular-nums">
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
            {segments.map((seg, i) =>
              seg.length > 1 ? <path key={`a${i}`} d={area(seg)} fill={color} opacity={0.1} /> : null,
            )}
            {segments.map((seg, i) =>
              seg.length > 1 ? (
                <path key={`l${i}`} d={line(seg)} fill="none" stroke={color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
              ) : (
                <circle key={`l${i}`} cx={x(seg[0][0])} cy={y(seg[0][1])} r={2.5} fill={color} />
              ),
            )}
            {thresholds.map((th) => (
              <g key={th.label}>
                <line
                  x1={M.left}
                  x2={M.left + plotW}
                  y1={y(th.value)}
                  y2={y(th.value)}
                  stroke="var(--text-tertiary)"
                  strokeWidth={1}
                  strokeDasharray="4 3"
                  shapeRendering="crispEdges"
                />
                <text x={M.left + plotW - 4} y={y(th.value) - 4} textAnchor="end" className="fill-[var(--text-secondary)] text-[11px]">
                  {th.label}
                </text>
              </g>
            ))}
            {markers
              .filter((m) => m.t >= t0 && m.t <= t1)
              .map((m) => (
                <g key={`${m.t}-${m.label}`}>
                  <line x1={x(m.t)} x2={x(m.t)} y1={M.top} y2={M.top + plotH} stroke="var(--text-tertiary)" strokeWidth={1} opacity={0.7} shapeRendering="crispEdges" />
                  <path d={`M${x(m.t)},${M.top - 1} l4,4 l-4,4 l-4,-4 z`} fill="var(--text-secondary)" stroke="var(--bg-secondary)" strokeWidth={1.5} />
                </g>
              ))}
          </g>
          {nearest && (
            <g pointerEvents="none">
              <line x1={x(nearest[0])} x2={x(nearest[0])} y1={M.top} y2={M.top + plotH} stroke="var(--text-secondary)" strokeWidth={1} shapeRendering="crispEdges" />
              <circle cx={x(nearest[0])} cy={y(nearest[1])} r={4} fill={color} stroke="var(--bg-secondary)" strokeWidth={2} />
            </g>
          )}
        </svg>
      )}
      {tipLeft !== null && (nearest || nearMarker) && (
        <div
          role="status"
          className="pointer-events-none absolute top-0 z-10 min-w-[8rem] max-w-[16rem] rounded-md border border-[var(--border-default)] bg-[var(--bg-elevated)] px-2.5 py-1.5 text-xs shadow-[var(--shadow-md)]"
          style={tipLeft > width / 2 ? { right: width - tipLeft + 10 } : { left: tipLeft + 10 }}
        >
          {nearest && (
            <>
              <div className="font-semibold tabular-nums text-[var(--text-primary)]">{format(nearest[1])}</div>
              <div className="tabular-nums text-[var(--text-tertiary)]">{fmtFull(nearest[0])}</div>
            </>
          )}
          {nearMarker && (
            <div className={cn("text-[var(--text-secondary)]", nearest && "mt-1 border-t border-[var(--border-subtle)] pt-1")}>
              <div className="font-medium text-[var(--text-primary)]">◆ {nearMarker.label}</div>
              {nearMarker.detail && <div className="line-clamp-2">{nearMarker.detail}</div>}
              {!nearest && <div className="tabular-nums text-[var(--text-tertiary)]">{fmtFull(nearMarker.t)}</div>}
            </div>
          )}
        </div>
      )}
    </div>
  );
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
