"use client";

import { useState, useEffect } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Trash2, Plus, Save, Eye, EyeOff } from "lucide-react";
import { useServiceEnv, useSetServiceEnv } from "@/features/services";
import type { KusoEnvVar } from "@/types/projects";
import { toast } from "sonner";

interface Row {
  name: string;
  value: string;
  fromSecret: boolean;
  visible: boolean;
}

function toRow(v: KusoEnvVar): Row {
  const fromSecret = !!v.valueFrom;
  return {
    name: v.name ?? "",
    value: fromSecret ? "" : (v.value ?? ""),
    fromSecret,
    visible: false,
  };
}

function toEnvVar(r: Row): KusoEnvVar {
  if (r.fromSecret) {
    // Phase C UI doesn't edit secret-backed entries; they pass through.
    return { name: r.name };
  }
  return { name: r.name, value: r.value };
}

export function EnvVarsEditor({ project, service }: { project: string; service: string }) {
  const env = useServiceEnv(project, service);
  const setEnv = useSetServiceEnv(project, service);
  const [rows, setRows] = useState<Row[]>([]);
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (env.data) {
      setRows((env.data.envVars ?? []).map(toRow));
      setDirty(false);
    }
  }, [env.data]);

  const update = (idx: number, patch: Partial<Row>) => {
    setRows((prev) => prev.map((r, i) => (i === idx ? { ...r, ...patch } : r)));
    setDirty(true);
  };
  const remove = (idx: number) => {
    setRows((prev) => prev.filter((_, i) => i !== idx));
    setDirty(true);
  };
  const add = () => {
    setRows((prev) => [...prev, { name: "", value: "", fromSecret: false, visible: true }]);
    setDirty(true);
  };

  const save = async () => {
    const cleaned = rows.filter((r) => r.name.trim().length > 0).map(toEnvVar);
    try {
      await setEnv.mutateAsync(cleaned);
      toast.success("Env vars saved");
      setDirty(false);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save env vars");
    }
  };

  if (env.isPending) {
    return <div className="text-sm text-[var(--text-tertiary)]">loading…</div>;
  }
  if (env.isError) {
    return (
      <div className="text-sm text-red-500">
        Failed to load env vars: {env.error?.message}
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <div className="space-y-2">
        {rows.length === 0 && (
          <p className="text-sm text-[var(--text-tertiary)]">
            No env vars. Add one to get started.
          </p>
        )}
        {rows.map((r, i) => (
          <div key={i} className="flex items-center gap-2">
            <Input
              placeholder="KEY"
              value={r.name}
              onChange={(e) => update(i, { name: e.target.value })}
              className="font-mono"
              disabled={r.fromSecret}
              spellCheck={false}
            />
            <Input
              placeholder={r.fromSecret ? "(from secret)" : "value"}
              type={r.visible || r.fromSecret ? "text" : "password"}
              value={r.value}
              onChange={(e) => update(i, { value: e.target.value })}
              className="font-mono flex-1"
              disabled={r.fromSecret}
              spellCheck={false}
            />
            {!r.fromSecret && (
              <Button
                variant="ghost"
                size="icon-sm"
                type="button"
                aria-label={r.visible ? "Hide" : "Show"}
                onClick={() => update(i, { visible: !r.visible })}
              >
                {r.visible ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
              </Button>
            )}
            <Button
              variant="ghost"
              size="icon-sm"
              type="button"
              aria-label="Remove"
              onClick={() => remove(i)}
              disabled={r.fromSecret}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </div>
        ))}
      </div>
      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" onClick={add} type="button">
          <Plus className="h-3.5 w-3.5" /> Add
        </Button>
        <Button
          size="sm"
          onClick={save}
          type="button"
          disabled={!dirty || setEnv.isPending}
        >
          <Save className="h-3.5 w-3.5" />
          {setEnv.isPending ? "Saving…" : "Save"}
        </Button>
        {dirty && (
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            unsaved changes
          </span>
        )}
      </div>
    </div>
  );
}
