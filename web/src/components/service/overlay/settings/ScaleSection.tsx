"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Layers3 } from "lucide-react";
import { Input } from "@/components/ui/input";
import { listPodSizes } from "@/features/cluster-config/api";
import { matchPodSize, podSizeFields } from "@/features/services";
import { Section, Row, type SectionProps } from "./_primitives";

const CUSTOM = "__custom";

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

  // Same cache key as the admin DefaultPodSizeSection.
  const sizes = useQuery({ queryKey: ["admin", "podsizes"], queryFn: listPodSizes });
  const presets = sizes.data ?? [];
  const matched = matchPodSize(presets, state);
  const [customOpen, setCustomOpen] = useState(false);
  const showCustom = customOpen || matched === null;
  const selectValue = showCustom ? CUSTOM : (matched ?? "");

  const onPick = (v: string) => {
    if (v === CUSTOM) {
      setCustomOpen(true);
      return;
    }
    setCustomOpen(false);
    const preset = presets.find((p) => p.Name === v);
    setState((s) => ({
      ...s,
      ...(preset
        ? podSizeFields(preset)
        : { cpuRequest: "", cpuLimit: "", memRequest: "", memLimit: "" }),
    }));
  };

  const selectedPreset = presets.find((p) => p.Name === matched);

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
      {autoscales && (
        <>
          <Row
            label="scale-up delay"
            hint="seconds of sustained load before adding pods"
            control={
              <Input
                type="number"
                value={state.scaleUpWindow}
                onChange={(e) => setState((s) => ({ ...s, scaleUpWindow: e.target.value }))}
                placeholder="120"
                className="h-7 w-20 font-mono text-[12px]"
                min={0}
                max={3600}
              />
            }
          />
          <Row
            label="scale-up step"
            hint="pods added per minute, or % of current pods — whichever is more"
            control={
              <div className="inline-flex items-center gap-1.5">
                <Input
                  type="number"
                  value={state.scaleUpPods}
                  onChange={(e) => setState((s) => ({ ...s, scaleUpPods: e.target.value }))}
                  placeholder="1"
                  aria-label="Pods added per minute"
                  className="h-7 w-16 font-mono text-[12px]"
                  min={1}
                  max={100}
                />
                <span className="font-mono text-[11px] text-[var(--text-tertiary)]">/</span>
                <Input
                  type="number"
                  value={state.scaleUpPercent}
                  onChange={(e) => setState((s) => ({ ...s, scaleUpPercent: e.target.value }))}
                  placeholder="off"
                  aria-label="Percent of current pods added per minute"
                  className="h-7 w-16 font-mono text-[12px]"
                  min={1}
                  max={1000}
                />
                <span className="font-mono text-[11px] text-[var(--text-tertiary)]">%</span>
              </div>
            }
          />
          <Row
            label="scale-down delay"
            hint="seconds of low load before removing pods"
            control={
              <Input
                type="number"
                value={state.scaleDownWindow}
                onChange={(e) => setState((s) => ({ ...s, scaleDownWindow: e.target.value }))}
                placeholder="300"
                className="h-7 w-20 font-mono text-[12px]"
                min={0}
                max={3600}
              />
            }
          />
        </>
      )}
      <Row
        label="pod size"
        hint={
          selectedPreset
            ? `${selectedPreset.CPURequest || "-"} / ${selectedPreset.MemoryRequest || "-"} guaranteed · ${selectedPreset.CPULimit || "-"} / ${selectedPreset.MemoryLimit || "-"} max`
            : matched === ""
              ? "no requests or limits set"
              : "CPU and memory per pod"
        }
        control={
          <select
            value={selectValue}
            onChange={(e) => onPick(e.target.value)}
            aria-label="Pod size"
            className="h-7 w-44 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 font-mono text-[12px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
          >
            <option value="">None</option>
            {presets.map((p) => (
              <option key={p.ID} value={p.Name}>
                {p.Name}
              </option>
            ))}
            <option value={CUSTOM}>Custom…</option>
          </select>
        }
        last={!showCustom}
      />
      {showCustom && (
        <>
          {/* k8s quantity syntax: cpu "100m"/"0.5"/"2", memory "128Mi"/"1Gi".
              Request = guaranteed floor (scheduling + HPA %); limit =
              hard ceiling (OOM-kill / CPU-throttle past it). */}
          <Row
            label="cpu request / limit"
            hint='e.g. "100m" / "1"'
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
            hint='e.g. "128Mi" / "512Mi"'
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
        </>
      )}
    </Section>
  );
}
