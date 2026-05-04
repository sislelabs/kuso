"use client";

import { Layers3 } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Section, Row, type SectionProps } from "./_primitives";

export function ScaleSection({ state, setState }: SectionProps) {
  const min = Number(state.scaleMin);
  const sleeps = min === 0;
  return (
    <Section
      id="scale"
      title="Scale"
      icon={Layers3}
      hint={sleeps ? "sleeps when idle" : `keeps ${min} pod${min === 1 ? "" : "s"} warm`}
    >
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
        hint="autoscale ceiling"
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
        last
      />
    </Section>
  );
}
