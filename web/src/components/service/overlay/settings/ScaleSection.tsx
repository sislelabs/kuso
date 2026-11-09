"use client";

import { Select } from "@/components/ui/select";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Layers3 } from "lucide-react";
import { Input } from "@/components/ui/input";
import { listPodSizes } from "@/features/cluster-config/api";
import { matchPodSize, podSizeFields } from "@/features/services";
import { Section, Field, FieldGrid, WithSuffix, monoInputClass, type SectionProps } from "./_primitives";

const CUSTOM = "__custom";

export function ScaleSection({ state, setState }: SectionProps) {
  const min = Number(state.scaleMin);
  const max = Number(state.scaleMax);
  const sleeps = min === 0;
  const autoscales = max > Math.max(min, 1);
  const hint = sleeps
    ? "Sleeps when idle"
    : autoscales
      ? `Autoscales ${min} → ${max}`
      : `${min} pod${min === 1 ? "" : "s"} always on`;

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

  const num = (field: "scaleMin" | "scaleMax" | "scaleCPU" | "scaleUpWindow" | "scaleUpPods" | "scaleUpPercent" | "scaleDownWindow", id: string, opts: { min?: number; max?: number; placeholder?: string; label?: string }) => (
    <Input
      id={id}
      type="number"
      inputMode="numeric"
      value={state[field]}
      onChange={(e) => setState((s) => ({ ...s, [field]: e.target.value }))}
      min={opts.min}
      max={opts.max}
      placeholder={opts.placeholder}
      aria-label={opts.label}
      className={monoInputClass}
    />
  );
  const quantity = (field: "cpuRequest" | "cpuLimit" | "memRequest" | "memLimit", id: string, placeholder: string, label: string) => (
    <Input
      id={id}
      aria-label={label}
      value={state[field]}
      onChange={(e) => setState((s) => ({ ...s, [field]: e.target.value }))}
      placeholder={placeholder}
      className={monoInputClass}
      spellCheck={false}
    />
  );

  return (
    <Section
      id="scale"
      title="Scale"
      icon={Layers3}
      status={hint}
      description="Replica count, CPU autoscaling and how much CPU and memory each pod gets."
      footerNote="Saving rolls the pods in every environment."
    >
      <FieldGrid cols={3}>
        <Field label="Min replicas" hint="0 lets the service sleep when idle." htmlFor="scale-min">
          {num("scaleMin", "scale-min", { min: 0 })}
        </Field>
        <Field label="Max replicas" hint="Above min turns on autoscaling." htmlFor="scale-max">
          {num("scaleMax", "scale-max", { min: 1 })}
        </Field>
        <Field label="CPU target" hint="Add a replica past this usage." htmlFor="scale-cpu">
          <WithSuffix suffix="%">{num("scaleCPU", "scale-cpu", { min: 1, max: 100 })}</WithSuffix>
        </Field>
      </FieldGrid>
      {autoscales && (
        <FieldGrid cols={3}>
          <Field label="Scale-up delay" hint="Sustained load before adding pods." htmlFor="scale-up-window">
            <WithSuffix suffix="sec">{num("scaleUpWindow", "scale-up-window", { min: 0, max: 3600, placeholder: "120" })}</WithSuffix>
          </Field>
          <Field
            label="Scale-up step"
            hint="Pods or % of current pods added per minute, whichever is more."
          >
            <div className="flex items-center gap-2">
              {num("scaleUpPods", "scale-up-pods", { min: 1, max: 100, placeholder: "1", label: "Pods added per minute" })}
              <span className="text-xs text-[var(--text-secondary)]">pods</span>
              {num("scaleUpPercent", "scale-up-pct", { min: 1, max: 1000, placeholder: "off", label: "Percent of current pods added per minute" })}
              <span className="text-xs text-[var(--text-secondary)]">%</span>
            </div>
          </Field>
          <Field label="Scale-down delay" hint="Low load before removing pods." htmlFor="scale-down-window">
            <WithSuffix suffix="sec">{num("scaleDownWindow", "scale-down-window", { min: 0, max: 3600, placeholder: "300" })}</WithSuffix>
          </Field>
        </FieldGrid>
      )}
      <Field
        label="Pod size"
        htmlFor="scale-pod-size"
        hint={
          selectedPreset ? (
            <>
              Guaranteed <span className="font-mono">{selectedPreset.CPURequest || "no"}</span> CPU and{" "}
              <span className="font-mono">{selectedPreset.MemoryRequest || "no"}</span> memory; capped at{" "}
              <span className="font-mono">{selectedPreset.CPULimit || "no limit"}</span> CPU and{" "}
              <span className="font-mono">{selectedPreset.MemoryLimit || "no limit"}</span> memory.
            </>
          ) : matched === "" ? (
            "No requests or limits: pods get whatever the node has spare."
          ) : (
            "CPU and memory per pod. Presets come from cluster settings."
          )
        }
      >
        <Select
          size="sm"
          id="scale-pod-size"
          value={selectValue}
          onChange={(e) => onPick(e.target.value)}
          wrapperClassName="sm:max-w-xs"
        >
          <option value="">None</option>
          {presets.map((p) => (
            <option key={p.ID} value={p.Name}>
              {p.Name}
            </option>
          ))}
          <option value={CUSTOM}>Custom…</option>
        </Select>
      </Field>
      {showCustom && (
        // k8s quantity syntax: request = guaranteed floor (scheduling +
        // HPA %), limit = hard ceiling (OOM-kill / throttle past it).
        <FieldGrid>
          <Field label="CPU request / limit" hint={<>Cores, e.g. <span className="font-mono">100m</span> or <span className="font-mono">1</span>.</>}>
            <div className="flex items-center gap-2">
              {quantity("cpuRequest", "scale-cpu-req", "auto", "CPU request, in cores")}
              <span aria-hidden className="text-xs text-[var(--text-tertiary)]">/</span>
              {quantity("cpuLimit", "scale-cpu-lim", "auto", "CPU limit, in cores")}
            </div>
          </Field>
          <Field label="Memory request / limit" hint={<>e.g. <span className="font-mono">128Mi</span> or <span className="font-mono">1Gi</span>.</>}>
            <div className="flex items-center gap-2">
              {quantity("memRequest", "scale-mem-req", "auto", "Memory request, in Mi or Gi")}
              <span aria-hidden className="text-xs text-[var(--text-tertiary)]">/</span>
              {quantity("memLimit", "scale-mem-lim", "auto", "Memory limit, in Mi or Gi")}
            </div>
          </Field>
        </FieldGrid>
      )}
    </Section>
  );
}
