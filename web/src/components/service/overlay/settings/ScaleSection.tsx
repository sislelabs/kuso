"use client";

import { Layers3 } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Section, Row, type SectionProps } from "./_primitives";

export function ScaleSection({ state, setState }: SectionProps) {
  const min = Number(state.scaleMin);
  const max = Number(state.scaleMax);
  const sleeps = min === 0;
  const autoscales = max > Math.max(min, 1);
  const hint = sleeps
    ? "sleeps when idle"
    : autoscales
      ? `autoscales ${min} → ${max} on CPU`
      : `keeps ${min} pod${min === 1 ? "" : "s"} warm`;
  return (
    <Section id="scale" title="Scale" icon={Layers3} hint={hint}>
      <Row
        label="min replicas"
        hint="0 = sleep when idle"
        control={
          <Input
            type="number"
            value={state.scaleMin}
            onChange={(e) => setState((s) => ({ ...s, scaleMin: e.target.value }))}
            className="h-7 w-20 font-mono text-[12px]"
            min={0}
          />
        }
      />
      <Row
        label="max replicas"
        hint={autoscales ? "HPA ceiling — set > min to autoscale" : "set this above min to enable autoscaling"}
        control={
          <Input
            type="number"
            value={state.scaleMax}
            onChange={(e) => setState((s) => ({ ...s, scaleMax: e.target.value }))}
            className="h-7 w-20 font-mono text-[12px]"
            min={1}
          />
        }
      />
      <Row
        label="cpu threshold"
        hint="add a replica past this %"
        control={
          <div className="inline-flex items-center gap-1.5">
            <Input
              type="number"
              value={state.scaleCPU}
              onChange={(e) => setState((s) => ({ ...s, scaleCPU: e.target.value }))}
              className="h-7 w-16 font-mono text-[12px]"
              min={1}
              max={100}
            />
            <span className="font-mono text-[11px] text-[var(--text-tertiary)]">%</span>
          </div>
        }
      />
      {/* Wake-on exclude paths — only relevant when the service sleeps.
          Any request to a listed path keeps the WHOLE deployment warm, so
          a webhook/callback on a sleeping service doesn't cold-start-503.
          Hidden when min>0 (nothing to protect — it never sleeps). */}
      {sleeps && (
        <Row
          label="keep-warm paths"
          hint="requests to these paths block sleep (webhooks/callbacks) — one per line"
          control={
            <textarea
              value={state.sleepExcludePaths}
              onChange={(e) => setState((s) => ({ ...s, sleepExcludePaths: e.target.value }))}
              placeholder={"/api/webhooks/stripe\n/api/callbacks/github"}
              rows={2}
              className="h-auto w-56 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1 font-mono text-[11px]"
            />
          }
        />
      )}
      {/* Pod resources. Blank = chart default. Request = guaranteed
          floor (drives scheduling + HPA %); limit = hard ceiling
          (OOM-kill / CPU-throttle past it). k8s quantity syntax:
          cpu "100m"/"0.5"/"2", memory "128Mi"/"1Gi". */}
      <Row
        label="cpu request / limit"
        hint='guaranteed / max — e.g. "100m" / "1"'
        control={
          <div className="inline-flex items-center gap-1.5">
            <Input
              value={state.cpuRequest}
              onChange={(e) => setState((s) => ({ ...s, cpuRequest: e.target.value }))}
              placeholder="auto"
              className="h-7 w-20 font-mono text-[12px]"
            />
            <span className="font-mono text-[11px] text-[var(--text-tertiary)]">/</span>
            <Input
              value={state.cpuLimit}
              onChange={(e) => setState((s) => ({ ...s, cpuLimit: e.target.value }))}
              placeholder="auto"
              className="h-7 w-20 font-mono text-[12px]"
            />
          </div>
        }
      />
      <Row
        label="memory request / limit"
        hint='guaranteed / max — e.g. "128Mi" / "512Mi"'
        control={
          <div className="inline-flex items-center gap-1.5">
            <Input
              value={state.memRequest}
              onChange={(e) => setState((s) => ({ ...s, memRequest: e.target.value }))}
              placeholder="auto"
              className="h-7 w-20 font-mono text-[12px]"
            />
            <span className="font-mono text-[11px] text-[var(--text-tertiary)]">/</span>
            <Input
              value={state.memLimit}
              onChange={(e) => setState((s) => ({ ...s, memLimit: e.target.value }))}
              placeholder="auto"
              className="h-7 w-20 font-mono text-[12px]"
            />
          </div>
        }
        last
      />
    </Section>
  );
}
