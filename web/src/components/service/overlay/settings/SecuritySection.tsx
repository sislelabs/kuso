"use client";

import { ShieldAlert } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Section, Field, SwitchField, Code, monoInputClass, type SectionProps } from "./_primitives";

// SecuritySection is an advanced/rarely-touched escape hatch: kuso pods
// run hardened by default (all Linux capabilities dropped, privilege
// escalation disabled). Some images self-drop root at runtime via
// setpriv/gosu/su-exec and need a capability back (e.g. SETUID/SETGID).
export function SecuritySection({ state, setState }: SectionProps) {
  const capCount = state.capAdd
    .split(",")
    .map((c) => c.trim())
    .filter(Boolean).length;
  const status =
    capCount === 0 && !state.allowPrivilegeEscalation
      ? "Hardened default"
      : [
          capCount > 0 ? `+${capCount} capabilit${capCount === 1 ? "y" : "ies"}` : null,
          state.allowPrivilegeEscalation ? "escalation allowed" : null,
        ]
          .filter(Boolean)
          .join(", ");

  return (
    <Section
      id="security"
      title="Security"
      icon={ShieldAlert}
      status={status}
      description="Every service runs with all Linux capabilities dropped and privilege escalation off. Only loosen this for images that drop root themselves at startup (setpriv, gosu, su-exec)."
      footerNote="Saving rolls the pods in every environment."
    >
      <Field
        label="Add capabilities"
        hint={
          <>
            Comma-separated, without the <Code>CAP_</Code> prefix.
          </>
        }
        htmlFor="security-cap-add"
      >
        <Input
          id="security-cap-add"
          value={state.capAdd}
          onChange={(e) => setState((s) => ({ ...s, capAdd: e.target.value }))}
          placeholder="e.g. SETUID, SETGID"
          className={monoInputClass}
          spellCheck={false}
        />
      </Field>
      <SwitchField
        label="Allow privilege escalation"
        hint="Lets a process gain more privileges than its parent, e.g. through setuid binaries."
        on={state.allowPrivilegeEscalation}
        onChange={(v) => setState((s) => ({ ...s, allowPrivilegeEscalation: v }))}
      />
    </Section>
  );
}
