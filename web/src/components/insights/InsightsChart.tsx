"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, PointerEvent } from "react";
import type { InsightsPoint } from "@/features/insights";
import { cn } from "@/lib/utils";

export interface ChartSeries {
  id: string;
  label: string;
  color: string;
  points: InsightsPoint[];
}

export interface ChartDeploy {
  /** unix seconds */
  at: number;
  color: string;
  title: string;
  detail: string;
}

const PAD = { left: 64, right: 12, top: 12, bottom: 44 };

function niceStep(max: number): number {
  const raw = max / 4;
  const mag = 10 ** Math.floor(Math.log10(raw || 1));
  return [1, 2, 2.5, 5, 10].map((s) => s * mag).find((s) => s >= raw) ?? 10 * mag;
}

function clock(t: number, opts: Intl.DateTimeFormatOptions): string {
  return new Date(t * 1000).toLocaleString(undefined, { hour12: false, ...opts });
}

function nearest(points: readonly InsightsPoint[], t: number): InsightsPoint | undefined {
  let best: InsightsPoint | undefined;
  for (const p of points) if (!best || Math.abs(p[0] - t) < Math.abs(best[0] - t)) best = p;
  return best;
}

// InsightsChart draws several series on one time axis with deploy
// markers, a crosshair and a tooltip. Arrow keys move the crosshair a
// step at a time; the table under the chart carries the same numbers
// for anyone not using the chart.
export function InsightsChart({
  series,
  deploys,
  domain,
  stepSeconds,
  format,
  tick,
  tickUnit = 1,
  ariaLabel,
  empty,
  dim,
}: {
  series: ChartSeries[];
  deploys: ChartDeploy[];
  /** unix seconds */
  domain: [number, number];
  stepSeconds: number;
  format: (v: number) => string;
  tick: (v: number) => string;
  tickUnit?: number;
  ariaLabel: string;
  empty?: string;
  dim?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(800);
  const [hoverT, setHoverT] = useState<number | null>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.max(280, Math.round(e.contentRect.width))));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const HEIGHT = width < 520 ? 260 : 380;
  const [t0, t1] = domain;
  const plotW = width - PAD.left - PAD.right;
  const plotH = HEIGHT - PAD.top - PAD.bottom;

  const { yMax, yStep } = useMemo(() => {
    const peak = Math.max(0, ...series.flatMap((s) => s.points.map((p) => p[1])));
    const step = niceStep(peak > 0 ? (peak * 1.08) / tickUnit : 1) * tickUnit;
    return { yStep: step, yMax: Math.max(step, Math.ceil((peak * 1.08) / step) * step) };
  }, [series, tickUnit]);

  const x = (t: number) => PAD.left + ((t - t0) / (t1 - t0 || 1)) * plotW;
  const y = (v: number) => PAD.top + plotH - (v / yMax) * plotH;

  // Clean intervals, thinned until labels sit at least 56px apart.
  const timeTick =
    [600, 900, 1800, 3600, 2 * 3600, 3 * 3600, 6 * 3600, 12 * 3600].find((s) => (s / (t1 - t0 || 1)) * plotW >= 56) ?? 24 * 3600;
  const xTicks: number[] = [];
  for (let t = Math.ceil(t0 / timeTick) * timeTick; t <= t1; t += timeTick) xTicks.push(t);
  const yTicks: number[] = [];
  for (let v = 0; v <= yMax + 1e-9; v += yStep) yTicks.push(v);

  const paths = series.map((s) => {
    // Break the line across gaps (no traffic, a restart) instead of
    // drawing a straight bridge over missing data.
    let d = "";
    let prev: number | null = null;
    for (const [t, v] of s.points) {
      const cmd = prev === null || t - prev > stepSeconds * 2.5 ? "M" : "L";
      d += `${cmd}${x(t).toFixed(1)},${y(v).toFixed(1)}`;
      prev = t;
    }
    return { ...s, d };
  });

  const onMove = (e: PointerEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const px = ((e.clientX - r.left) / r.width) * width;
    if (px < PAD.left || px > width - PAD.right) return setHoverT(null);
    setHoverT(t0 + ((px - PAD.left) / plotW) * (t1 - t0));
  };
  const onKey = (e: KeyboardEvent<SVGSVGElement>) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    const dir = e.key === "ArrowLeft" ? -1 : 1;
    setHoverT((t) => Math.min(t1, Math.max(t0, (t ?? t1) + dir * stepSeconds)));
  };

  const near = hoverT === null ? [] : deploys.filter((d) => Math.abs(d.at - hoverT) <= Math.max(stepSeconds, (t1 - t0) / 96));
  const hx = hoverT === null ? 0 : x(hoverT);
  const tipLeft = hx > width * 0.6;
  const hasData = series.some((s) => s.points.length > 0);

  return (
    <div ref={ref} className={cn("relative transition-opacity", dim && "opacity-60")}>
      <svg
        role="img"
        aria-label={ariaLabel}
        tabIndex={0}
        width={width}
        height={HEIGHT}
        viewBox={`0 0 ${width} ${HEIGHT}`}
        className="block touch-pan-y rounded-md outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
        onPointerMove={onMove}
        onPointerLeave={() => setHoverT(null)}
        onKeyDown={onKey}
        onBlur={() => setHoverT(null)}
      >
        {yTicks.map((v) => (
          <g key={v}>
            <line x1={PAD.left} x2={width - PAD.right} y1={y(v)} y2={y(v)} className="stroke-[var(--border-subtle)]" />
            <text x={PAD.left - 8} y={y(v) + 4} textAnchor="end" className="fill-[var(--text-tertiary)] font-mono text-[10.5px]">
              {v === 0 ? "0" : tick(v)}
            </text>
          </g>
        ))}
        {xTicks.map((t) => (
          <text key={t} x={x(t)} y={HEIGHT - PAD.bottom + 18} textAnchor="middle" className="fill-[var(--text-tertiary)] font-mono text-[10.5px]">
            {clock(t, { hour: "2-digit", minute: "2-digit" })}
          </text>
        ))}

        {paths.map((s) => (
          <g key={s.id}>
            {series.length <= 2 && s.points.length > 1 && (
              <path
                d={`${s.d}L${x(s.points[s.points.length - 1][0])},${y(0)}L${x(s.points[0][0])},${y(0)}Z`}
                fill={s.color}
                opacity={0.08}
              />
            )}
            <path d={s.d} fill="none" stroke={s.color} strokeWidth={1.7} strokeLinejoin="round" />
          </g>
        ))}

        {deploys.map((d, i) => {
          const X = x(d.at);
          const Y = HEIGHT - PAD.bottom + 30;
          return (
            <g key={`${d.at}-${i}`}>
              <line x1={X} x2={X} y1={PAD.top} y2={HEIGHT - PAD.bottom} stroke={d.color} strokeOpacity={0.4} strokeDasharray="2 3" />
              <rect
                x={X - 4.5}
                y={Y - 4.5}
                width={9}
                height={9}
                transform={`rotate(45 ${X} ${Y})`}
                fill={d.color}
                className="stroke-[var(--bg-secondary)]"
                strokeWidth={1.5}
              />
            </g>
          );
        })}

        {!hasData && (
          <text x={width / 2} y={HEIGHT / 2} textAnchor="middle" className="fill-[var(--text-tertiary)] text-sm">
            {empty ?? "No data in this range"}
          </text>
        )}

        {hoverT !== null && (
          <g>
            <line x1={hx} x2={hx} y1={PAD.top} y2={HEIGHT - PAD.bottom} className="stroke-[var(--border-strong)]" />
            {series.map((s) => {
              const p = nearest(s.points, hoverT);
              if (!p || Math.abs(p[0] - hoverT) > stepSeconds * 2) return null;
              return <circle key={s.id} cx={x(p[0])} cy={y(p[1])} r={3.5} fill={s.color} className="stroke-[var(--bg-secondary)]" strokeWidth={1.5} />;
            })}
          </g>
        )}
      </svg>

      {hoverT !== null && hasData && (
        <div
          aria-hidden
          className="pointer-events-none absolute top-3 z-10 min-w-52 max-w-80 rounded-lg border border-[var(--border-default)] bg-[var(--bg-elevated)] px-3 py-2 text-xs shadow-[var(--shadow-md)]"
          style={tipLeft ? { right: width - hx + 14 } : { left: hx + 14 }}
        >
          <div className="mb-1 text-[11px] text-[var(--text-tertiary)]">{clock(hoverT, { weekday: "short", hour: "2-digit", minute: "2-digit" })}</div>
          {series.map((s) => {
            const p = nearest(s.points, hoverT);
            const ok = p && Math.abs(p[0] - hoverT) <= stepSeconds * 2;
            return (
              <div key={s.id} className="flex items-center gap-2 py-px">
                <span className="size-2 shrink-0 rounded-full" style={{ background: s.color }} />
                <span className="truncate text-[var(--text-secondary)]">{s.label}</span>
                <span className="ml-auto pl-3 font-mono text-[var(--text-primary)]">{ok ? format(p[1]) : "—"}</span>
              </div>
            );
          })}
          {near.map((d, i) => (
            <div key={i} className="mt-1.5 border-t border-[var(--border-subtle)] pt-1.5">
              <div className="text-[var(--accent)]">◆ {d.title}</div>
              {d.detail && <div className="truncate text-[var(--text-tertiary)]">{d.detail}</div>}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
