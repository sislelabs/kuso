"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SettingsPage, SettingsPageLoading } from "@/components/settings/SettingsPage";
import { SettingsField, SettingsNotice, SettingsSection } from "@/components/settings/SettingsSection";
import { toast } from "sonner";
import { buildSettingsPayload, type BuildSettingsResponse } from "./payload";

type BuildSettings = Pick<
  BuildSettingsResponse,
  "maxConcurrent" | "memoryLimit" | "memoryRequest" | "cpuLimit" | "cpuRequest"
>;

// Sizing presets. Two invariants every preset must satisfy on the
// stated VM:
//   1. cap × memLimit ≤ vmRAM − 1 Gi      (1 Gi reserved for
//      kuso-server, traefik, cert-manager, addons overhead)
//   2. cap × cpuLimit ≤ vmCPU − 500m       (500m reserved so
//      kuso-server's API + the operator's reconcile loop don't
//      starve under build pressure)
// If you change these, re-derive the headroom calc in the card copy
// (the cards display the math so users can see why cap=N).
//
// nixpacks builds peak around 1.5 GB during the kaniko /nix
// snapshot, so we never go below memLimit=2Gi. Single-build
// presets prefer larger limits over parallelism — queueing is free,
// OOMKills aren't.
type VMSpec = { ramGi: number; vCPU: number };
const SMALL: VMSpec = { ramGi: 4, vCPU: 2 };
const MEDIUM: VMSpec = { ramGi: 8, vCPU: 4 };
const LARGE: VMSpec = { ramGi: 16, vCPU: 8 };

const PRESETS: { label: string; size: string; vm: VMSpec; values: BuildSettings }[] = [
  {
    label: "Small (4 GB / 2 vCPU)",
    size: "CCX13 / e2-small / t3.medium",
    vm: SMALL,
    // cap=1: 1 × 2.5Gi = 2.5 Gi ≤ 3 Gi ✓, 1 × 1500m = 1500m ≤ 1500m ✓
    // cap=2 with 1.5Gi each is fragile (nixpacks peak 1.5Gi, no margin).
    values: {
      maxConcurrent: 1,
      memoryRequest: "512Mi",
      memoryLimit:   "2500Mi",
      cpuRequest:    "200m",
      cpuLimit:      "1500m",
    },
  },
  {
    label: "Medium (8 GB / 4 vCPU)",
    size: "CCX23 / e2-standard-2",
    vm: MEDIUM,
    // cap=2: 2 × 2.5Gi = 5 Gi ≤ 7 Gi ✓, 2 × 1500m = 3000m ≤ 3500m ✓
    values: {
      maxConcurrent: 2,
      memoryRequest: "768Mi",
      memoryLimit:   "2500Mi",
      cpuRequest:    "300m",
      cpuLimit:      "1500m",
    },
  },
  {
    label: "Large (16 GB / 8 vCPU)",
    size: "CCX33 / e2-standard-4",
    vm: LARGE,
    // cap=4: 4 × 3Gi = 12 Gi ≤ 14 Gi ✓, 4 × 1500m = 6000m ≤ 7000m ✓
    values: {
      maxConcurrent: 4,
      memoryRequest: "1Gi",
      memoryLimit:   "3Gi",
      cpuRequest:    "500m",
      cpuLimit:      "1500m",
    },
  },
];

// parseQty is a forgiving numeric extraction from kube quantity
// strings — "2Gi" → 2, "1500m" → 1500, "1G" → 1 (treated as Gi here
// because we only round to display the math, not allocate). Returns
// 0 for empty / unparseable input.
function parseQtyRaw(v: string): number {
  const m = v.trim().match(/^([0-9]*\.?[0-9]+)/);
  return m ? parseFloat(m[1]) : 0;
}
// memGi: convert a quantity string to Gi for headroom math.
function memGi(v: string): number {
  const n = parseQtyRaw(v);
  if (!n) return 0;
  if (/Gi$/i.test(v)) return n;
  if (/G$/i.test(v))  return n * 1.073741824 / 1.073741824; // ~Gi
  if (/Mi$/i.test(v)) return n / 1024;
  if (/M$/i.test(v))  return n / 1024;
  return n / (1024 * 1024 * 1024);
}
// cpuM: convert a quantity string to millicores.
function cpuM(v: string): number {
  const n = parseQtyRaw(v);
  if (!n) return 0;
  if (/m$/.test(v)) return n;
  return n * 1000;
}

export default function BuildSettingsPage() {
  const [loaded, setLoaded] = useState(false);
  // A failed load leaves placeholder defaults in the form; saving them
  // would overwrite the real settings, so Save stays off until a reload.
  const [loadFailed, setLoadFailed] = useState(false);
  const [s, setS] = useState<BuildSettingsResponse>({
    maxConcurrent: 1,
    memoryLimit: "2Gi",
    memoryRequest: "512Mi",
    cpuLimit: "1500m",
    cpuRequest: "200m",
  });
  const [saving, setSaving] = useState(false);
  const [capEdited, setCapEdited] = useState(false);

  useEffect(() => {
    api<BuildSettingsResponse>("/api/admin/settings/build")
      .then((d) => {
        setS(d);
        setLoaded(true);
      })
      .catch((e) => {
        toast.error(e instanceof Error ? e.message : "Failed to load settings");
        setLoadFailed(true);
        setLoaded(true);
      });
  }, []);

  const save = async () => {
    setSaving(true);
    try {
      await api("/api/admin/settings/build", {
        method: "PUT",
        body: buildSettingsPayload(s, capEdited),
      });
      toast.success("Saved. New limits apply to the next build.");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  if (!loaded) {
    return <SettingsPageLoading title="Builds" rows={2} />;
  }

  const totalMemGi = (memGi(s.memoryLimit) * s.maxConcurrent).toFixed(1);
  const totalCpuM = cpuM(s.cpuLimit) * s.maxConcurrent;

  return (
    <SettingsPage
      title="Builds"
      description={
        <>
          How much CPU and memory each build can use, and how many builds run at once. Changes apply to the{" "}
          <em>next</em> build; in-flight builds keep their original limits.
        </>
      }
    >
      {loadFailed && (
        <SettingsNotice tone="error" title="Current settings failed to load">
          Saving is disabled so placeholder values don&apos;t overwrite the real ones. Reload the page to try again.
        </SettingsNotice>
      )}

      <SettingsSection
        title="Presets"
        description={
          <>
            Builds are CPU- and memory-heavy: nixpacks projects need about 2 GB per concurrent build. Going over your
            VM&apos;s limit makes builds OOMKill; undersizing leaves capacity unused. Pick one to fill the form below.
          </>
        }
        footerHint="Budget = VM size minus 1 Gi and 500m reserved for kuso-server, Traefik, cert-manager and addons. Going over is allowed but risks OOMKills and CPU starvation under load."
      >
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
          {PRESETS.map((p) => {
            // Show the math so the user can see why cap=N is what it
            // is for this VM size.
            const memUsedGi = memGi(p.values.memoryLimit) * p.values.maxConcurrent;
            const cpuUsedM = cpuM(p.values.cpuLimit) * p.values.maxConcurrent;
            const memReservedGi = 1; // for kuso-server + addons + traefik
            const cpuReservedM = 500;
            const memBudgetGi = p.vm.ramGi - memReservedGi;
            const cpuBudgetM = p.vm.vCPU * 1000 - cpuReservedM;
            return (
              <button
                key={p.label}
                type="button"
                onClick={() => {
                  setS({ ...s, ...p.values });
                  setCapEdited(true);
                }}
                className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3 text-left transition-colors hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]/40"
              >
                <div className="text-sm font-medium text-[var(--text-primary)]">{p.label}</div>
                <div className="mt-0.5 text-xs text-[var(--text-tertiary)]">{p.size}</div>
                <div className="mt-2 text-xs text-[var(--text-secondary)]">
                  {p.values.maxConcurrent} concurrent · <span className="font-mono">{p.values.memoryLimit}</span> ·{" "}
                  <span className="font-mono">{p.values.cpuLimit}</span> CPU
                </div>
                <div className="mt-1 text-xs text-[var(--text-tertiary)]">
                  Uses {memUsedGi.toFixed(1)} of {memBudgetGi} Gi · {cpuUsedM} of {cpuBudgetM}m CPU
                </div>
              </button>
            );
          })}
        </div>
      </SettingsSection>

      <SettingsSection
        title="Limits"
        description="Cluster-wide concurrency and per-build resources."
        footerHint={
          <>
            Quantities like <span className="font-mono">2Gi</span>, <span className="font-mono">1500m</span>,{" "}
            <span className="font-mono">500Mi</span>. Validated server-side.
          </>
        }
        footer={
          <Button
            size="sm"
            onClick={save}
            disabled={saving || loadFailed}
            title={loadFailed ? "Current settings failed to load. Reload the page to edit." : undefined}
          >
            {saving ? "Saving…" : "Save"}
          </Button>
        }
      >
        <SettingsField
          label="Concurrent builds"
          htmlFor="build-cap"
          hint={
            <>
              {s.maxConcurrentSet === false && !capEdited && "Not set: the server sizes it automatically. "}
              Cluster-wide cap on simultaneous build pods. Currently budgets {totalMemGi} Gi RAM and {totalCpuM}m CPU in
              total. 0 disables the cap (not recommended on a single-VM install).
            </>
          }
        >
          <Input
            id="build-cap"
            type="number"
            min={0}
            max={32}
            value={s.maxConcurrent}
            onChange={(e) => {
              setS({ ...s, maxConcurrent: parseInt(e.target.value || "0", 10) });
              setCapEdited(true);
            }}
            className="max-w-40 font-mono"
          />
        </SettingsField>

        <div className="grid grid-cols-1 gap-5 sm:grid-cols-2">
          <SettingsField
            label="Memory limit"
            htmlFor="build-mem-limit"
            hint="Hard cap on memory for one build. nixpacks peaks around 1.5 GB; 2 GB is a safe floor."
          >
            <QtyInput id="build-mem-limit" value={s.memoryLimit} onChange={(v) => setS({ ...s, memoryLimit: v })} />
          </SettingsField>
          <SettingsField
            label="Memory request"
            htmlFor="build-mem-req"
            hint="Memory the scheduler guarantees. Below the limit gives Burstable QoS."
          >
            <QtyInput id="build-mem-req" value={s.memoryRequest} onChange={(v) => setS({ ...s, memoryRequest: v })} />
          </SettingsField>
          <SettingsField
            label="CPU limit"
            htmlFor="build-cpu-limit"
            hint="Max CPU for one build. 1500m = 1.5 cores. Keep it below your core count."
          >
            <QtyInput id="build-cpu-limit" value={s.cpuLimit} onChange={(v) => setS({ ...s, cpuLimit: v })} />
          </SettingsField>
          <SettingsField
            label="CPU request"
            htmlFor="build-cpu-req"
            hint="Reserved CPU. 200m covers clone and push; the burst happens during the image snapshot."
          >
            <QtyInput id="build-cpu-req" value={s.cpuRequest} onChange={(v) => setS({ ...s, cpuRequest: v })} />
          </SettingsField>
        </div>
      </SettingsSection>
    </SettingsPage>
  );
}

function QtyInput({ id, value, onChange }: { id: string; value: string; onChange: (v: string) => void }) {
  return (
    <Input
      id={id}
      type="text"
      value={value}
      placeholder="e.g. 2Gi"
      spellCheck={false}
      onChange={(e) => onChange(e.target.value)}
      className="font-mono"
    />
  );
}
