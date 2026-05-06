"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { Cpu, MemoryStick } from "lucide-react";

interface BuildSettings {
  maxConcurrent: number;
  memoryLimit: string;
  memoryRequest: string;
  cpuLimit: string;
  cpuRequest: string;
}

// Sizing presets matched against typical Hetzner CCX boxes (the
// install path most users follow). Click to fill in the form. Each
// preset assumes one parallel build per ~3 GB of headroom + a
// kaniko ceiling that survives nixpacks /nix snapshots.
const PRESETS: { label: string; size: string; values: BuildSettings }[] = [
  {
    label: "Small (4 GB / 2 vCPU)",
    size: "CCX13 / e2-small / t3.medium",
    values: {
      maxConcurrent: 1,
      memoryRequest: "512Mi",
      memoryLimit:   "2Gi",
      cpuRequest:    "200m",
      cpuLimit:      "1500m",
    },
  },
  {
    label: "Medium (8 GB / 4 vCPU)",
    size: "CCX23 / e2-standard-2",
    values: {
      maxConcurrent: 2,
      memoryRequest: "768Mi",
      memoryLimit:   "3Gi",
      cpuRequest:    "500m",
      cpuLimit:      "2000m",
    },
  },
  {
    label: "Large (16 GB / 8 vCPU)",
    size: "CCX33 / e2-standard-4",
    values: {
      maxConcurrent: 4,
      memoryRequest: "1Gi",
      memoryLimit:   "4Gi",
      cpuRequest:    "1000m",
      cpuLimit:      "4000m",
    },
  },
];

export default function BuildSettingsPage() {
  const [loaded, setLoaded] = useState(false);
  const [s, setS] = useState<BuildSettings>({
    maxConcurrent: 1,
    memoryLimit: "2Gi",
    memoryRequest: "512Mi",
    cpuLimit: "1500m",
    cpuRequest: "200m",
  });
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    api<BuildSettings>("/api/admin/settings/build")
      .then((d) => {
        setS(d);
        setLoaded(true);
      })
      .catch((e) => {
        toast.error(e instanceof Error ? e.message : "Failed to load settings");
        setLoaded(true);
      });
  }, []);

  const save = async () => {
    setSaving(true);
    try {
      await api("/api/admin/settings/build", { method: "PUT", body: s });
      toast.success("Saved. New limits apply to the next build.");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  if (!loaded) {
    return (
      <div className="mx-auto max-w-3xl p-6 lg:p-8 space-y-4">
        <Skeleton className="h-10 w-1/3" />
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-3xl p-6 lg:p-8 space-y-8">
      <header>
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Build resources</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          How much CPU and memory each build pod can use, and how many builds
          can run at the same time. Changes apply to the <em>next</em> build —
          in-flight builds keep their original limits.
        </p>
      </header>

      {/* Sizing guidance */}
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
        <h2 className="mb-2 text-sm font-semibold">Pick a preset for your VM size</h2>
        <p className="mb-3 text-xs text-[var(--text-secondary)]">
          Builds are CPU- and memory-heavy. nixpacks projects in particular
          need ~2 GB per concurrent build (kaniko has to tar the populated
          /nix store). Going over your VM&apos;s limit makes builds OOMKill;
          undersizing leaves capacity on the table.
        </p>
        <div className="grid gap-2 sm:grid-cols-3">
          {PRESETS.map((p) => (
            <button
              key={p.label}
              type="button"
              onClick={() => setS(p.values)}
              className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3 text-left hover:border-[var(--accent)]/40 hover:bg-[var(--accent)]/5"
            >
              <div className="text-sm font-medium">{p.label}</div>
              <div className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">{p.size}</div>
              <div className="mt-2 font-mono text-[10px] text-[var(--text-secondary)]">
                cap {p.values.maxConcurrent} · {p.values.memoryLimit} mem · {p.values.cpuLimit} cpu
              </div>
            </button>
          ))}
        </div>
      </section>

      <section className="space-y-6">
        <FieldRow
          icon={Cpu}
          label="Concurrent builds"
          hint="Cluster-wide cap on simultaneous build pods. 0 disables the cap (not recommended on a single-VM install)."
        >
          <input
            type="number"
            min={0}
            max={32}
            value={s.maxConcurrent}
            onChange={(e) => setS({ ...s, maxConcurrent: parseInt(e.target.value || "0", 10) })}
            className="w-24 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1 font-mono"
          />
        </FieldRow>

        <FieldRow
          icon={MemoryStick}
          label="Memory limit (per build)"
          hint="Hard cap on resident memory for one kaniko Job pod. nixpacks builds peak around 1.5 GB during snapshot — set 2 GB unless you've measured otherwise."
        >
          <QtyInput value={s.memoryLimit} onChange={(v) => setS({ ...s, memoryLimit: v })} />
        </FieldRow>

        <FieldRow
          icon={MemoryStick}
          label="Memory request"
          hint="Reserved memory the scheduler guarantees. Setting this below the limit gives you Burstable QoS — survives host pressure without pre-allocating capacity."
        >
          <QtyInput value={s.memoryRequest} onChange={(v) => setS({ ...s, memoryRequest: v })} />
        </FieldRow>

        <FieldRow
          icon={Cpu}
          label="CPU limit"
          hint="Max CPU one build can use, in millicores. 1500m = 1.5 cores. Keep below your VM's core count so kuso-server doesn't starve."
        >
          <QtyInput value={s.cpuLimit} onChange={(v) => setS({ ...s, cpuLimit: v })} />
        </FieldRow>

        <FieldRow
          icon={Cpu}
          label="CPU request"
          hint="Reserved CPU. 200m is fine for the slow steps (clone, push); the burst happens during nix-env / kaniko snapshot."
        >
          <QtyInput value={s.cpuRequest} onChange={(v) => setS({ ...s, cpuRequest: v })} />
        </FieldRow>
      </section>

      <div className="flex items-center justify-between border-t border-[var(--border-subtle)] pt-4">
        <p className="text-xs text-[var(--text-tertiary)]">
          Quantity strings: <span className="font-mono">"2Gi" · "1500m" · "1G" · "500Mi"</span>.
          Validated server-side.
        </p>
        <Button onClick={save} disabled={saving}>
          {saving ? "Saving…" : "Save"}
        </Button>
      </div>
    </div>
  );
}

function FieldRow({
  icon: Icon,
  label,
  hint,
  children,
}: {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  hint: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-start justify-between gap-6">
      <div className="flex flex-1 items-start gap-3">
        <Icon className="mt-0.5 h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
        <div>
          <div className="text-sm font-medium">{label}</div>
          <div className="mt-0.5 text-xs text-[var(--text-secondary)]">{hint}</div>
        </div>
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}

function QtyInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <input
      type="text"
      value={value}
      placeholder="2Gi"
      onChange={(e) => onChange(e.target.value)}
      className="w-24 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1 font-mono text-sm"
    />
  );
}
