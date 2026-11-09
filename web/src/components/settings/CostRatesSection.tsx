"use client";

import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Save } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { updateClusterSettings } from "@/features/cluster-config/api";
import { useCan, Perms } from "@/features/auth";
import { toastError } from "@/lib/errors";
import { SettingsField, SettingsSection } from "@/components/settings/SettingsSection";

export interface CostRates {
  cpuPerHour: number;
  memGBPerHour: number;
  currency: string;
}

// parseRate accepts "0.04" or "" (unset → 0); anything else is an error.
export function parseRate(v: string): number | null {
  const t = v.trim();
  if (t === "") return 0;
  if (!/^\d*\.?\d+$/.test(t)) return null;
  const n = Number(t);
  return Number.isFinite(n) && n >= 0 ? n : null;
}

const CURRENCY_RE = /^[A-Z]{3}$/;

// CostRatesSection edits spec.cost on the Kuso CR, the rates the usage
// endpoints multiply CPU-hours and memory GB-hours by. POST /api/config
// merges by top-level key, so the whole cost object is sent.
export function CostRatesSection({ rates, id }: { rates: CostRates; id?: string }) {
  const canEdit = useCan(Perms.SettingsAdmin);
  const qc = useQueryClient();
  const [cpu, setCpu] = useState("");
  const [mem, setMem] = useState("");
  const [currency, setCurrency] = useState("USD");
  useEffect(() => {
    setCpu(rates.cpuPerHour ? String(rates.cpuPerHour) : "");
    setMem(rates.memGBPerHour ? String(rates.memGBPerHour) : "");
    setCurrency(rates.currency || "USD");
  }, [rates.cpuPerHour, rates.memGBPerHour, rates.currency]);

  const cpuN = parseRate(cpu);
  const memN = parseRate(mem);
  const cur = currency.trim().toUpperCase();
  const curOk = CURRENCY_RE.test(cur);
  const dirty = cpuN !== rates.cpuPerHour || memN !== rates.memGBPerHour || cur !== (rates.currency || "USD");
  const valid = cpuN !== null && memN !== null && curOk;

  const save = useMutation({
    mutationFn: () => updateClusterSettings({ cost: { cpuPerHour: cpuN ?? 0, memGBPerHour: memN ?? 0, currency: cur } }),
    onSuccess: () => {
      toast.success("Cost rates saved");
      void qc.invalidateQueries({ queryKey: ["usage"] });
      void qc.invalidateQueries({ queryKey: ["admin", "settings"] });
    },
    onError: (e) => toastError(e, "Couldn't save the rates"),
  });

  return (
    <SettingsSection
      id={id}
      title="Cost rates"
      description="What one CPU-hour and one GB-hour of memory cost you. Usage is multiplied by these to show cost per project."
      footerHint={!canEdit ? "Changing rates needs the settings:admin permission." : undefined}
      footer={
        <Button
          size="sm"
          onClick={() => save.mutate()}
          disabled={!canEdit || !dirty || !valid || save.isPending}
          disabledReason={!canEdit ? "Requires settings:admin" : !valid ? "Fix the highlighted fields" : !dirty ? "No changes to save" : undefined}
        >
          <Save className="h-3 w-3" />
          {save.isPending ? "Saving…" : "Save rates"}
        </Button>
      }
    >
      <form
        className="grid grid-cols-1 gap-5 sm:grid-cols-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (canEdit && dirty && valid) save.mutate();
        }}
      >
        <SettingsField
          label="Per CPU-hour"
          hint="One core for one hour. Blank = 0."
          htmlFor="cost-cpu"
          error={cpuN === null ? "Enter a number, like 0.04" : undefined}
        >
          <Input id="cost-cpu" inputMode="decimal" value={cpu} onChange={(e) => setCpu(e.target.value)} placeholder="e.g. 0.04" disabled={!canEdit} className="font-mono" />
        </SettingsField>
        <SettingsField
          label="Per memory GB-hour"
          hint="One GB of memory for one hour. Blank = 0."
          htmlFor="cost-mem"
          error={memN === null ? "Enter a number, like 0.005" : undefined}
        >
          <Input id="cost-mem" inputMode="decimal" value={mem} onChange={(e) => setMem(e.target.value)} placeholder="e.g. 0.005" disabled={!canEdit} className="font-mono" />
        </SettingsField>
        <SettingsField
          label="Currency"
          hint="Three-letter code."
          htmlFor="cost-currency"
          error={!curOk ? "Use a code like USD or EUR" : undefined}
        >
          <Input
            id="cost-currency"
            value={currency}
            onChange={(e) => setCurrency(e.target.value)}
            maxLength={3}
            placeholder="USD"
            disabled={!canEdit}
            className="font-mono uppercase"
          />
        </SettingsField>
        <button type="submit" hidden />
      </form>
    </SettingsSection>
  );
}
