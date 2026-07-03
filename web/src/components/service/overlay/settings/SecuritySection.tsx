"use client";

import { ShieldAlert } from "lucide-react";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { Section, Row, type SectionProps } from "./_primitives";

// SecuritySection is an advanced/rarely-touched escape hatch: kuso pods
// run hardened by default (all Linux capabilities dropped, privilege
// escalation disabled). Some images self-drop root at runtime via
// setpriv/gosu/su-exec and need a capability back (e.g. SETUID/SETGID)
// to do that dance — this section is how a user opts one service back
// into a narrower slice of that surface without touching raw k8s yaml.
export function SecuritySection({ state, setState }: SectionProps) {
  const capCount = state.capAdd
    .split(",")
    .map((c) => c.trim())
    .filter(Boolean).length;
  const hint =
    capCount === 0 && !state.allowPrivilegeEscalation
      ? "hardened default"
      : [
          capCount > 0 ? `+${capCount} cap${capCount === 1 ? "" : "s"}` : null,
          state.allowPrivilegeEscalation ? "escalation allowed" : null,
        ]
          .filter(Boolean)
          .join(", ");

  return (
    <Section
      id="security"
      title="Security"
      icon={ShieldAlert}
      hint={hint}
    >
      <div className="px-3 py-2.5 text-[11px] text-[var(--text-secondary)]">
        Every service runs with all Linux capabilities dropped and privilege
        escalation disabled. These are an escape hatch for images that
        self-drop root at runtime (setpriv/gosu/su-exec) and need a
        capability back to do it — most services should leave this alone.
      </div>
      <Row
        label="add capabilities"
        hint="comma-separated, without CAP_ — e.g. SETUID, SETGID"
        control={
          <Input
            value={state.capAdd}
            onChange={(e) => setState((s) => ({ ...s, capAdd: e.target.value }))}
            placeholder="SETUID, SETGID"
            className="h-7 w-56 font-mono text-[12px]"
          />
        }
      />
      <Row
        label="allow privilege escalation"
        hint="lets a process gain more privileges than its parent (setuid binaries)"
        control={
          <button
            type="button"
            onClick={() =>
              setState((s) => ({
                ...s,
                allowPrivilegeEscalation: !s.allowPrivilegeEscalation,
              }))
            }
            aria-pressed={state.allowPrivilegeEscalation}
            aria-label="Toggle allow privilege escalation"
            className={cn(
              "inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors",
              state.allowPrivilegeEscalation
                ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)]"
                : "border-[var(--border-subtle)] bg-[var(--bg-tertiary)]",
            )}
          >
            <span
              className={cn(
                "inline-block h-3.5 w-3.5 rounded-full bg-white shadow transition-transform",
                state.allowPrivilegeEscalation ? "translate-x-4" : "translate-x-0.5",
              )}
            />
          </button>
        }
        last
      />
    </Section>
  );
}
