"use client";

import { Rocket } from "lucide-react";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { Section, Field, SwitchField, WithSuffix, monoInputClass, type SectionProps } from "./_primitives";

// ReleaseSection configures the pre-deploy release hook: a one-off Job
// that runs before a rollout is promoted (migrations, etc). Empty
// command = no hook (the default for every service).
export function ReleaseSection({ state, setState }: SectionProps) {
  const hasHook = state.releaseCommand.trim().length > 0;

  return (
    <Section
      id="release"
      title="Release hook"
      icon={Rocket}
      status={hasHook ? "Runs before each deploy" : "No hook"}
      description="A one-off job that runs with the new image and the service's variables before the rollout is promoted, e.g. database migrations. A non-zero exit fails the deploy and the old version keeps serving."
      footerNote="Applies from the next deploy."
    >
      <Field label="Command" hint="Blank means no hook." htmlFor="release-command">
        <Input
          id="release-command"
          value={state.releaseCommand}
          onChange={(e) => setState((s) => ({ ...s, releaseCommand: e.target.value }))}
          placeholder="e.g. npm run migrate"
          className={monoInputClass}
          spellCheck={false}
        />
      </Field>
      <Field label="Timeout" hint="Blank uses the server default of 900 seconds." htmlFor="release-timeout">
        <WithSuffix suffix="seconds">
          <Input
            id="release-timeout"
            type="number"
            inputMode="numeric"
            value={state.releaseTimeout}
            onChange={(e) => setState((s) => ({ ...s, releaseTimeout: e.target.value }))}
            placeholder="900"
            className={cn(monoInputClass)}
            min={1}
          />
        </WithSuffix>
      </Field>
      <SwitchField
        label="Snapshot databases first"
        hint="Back up this service's Postgres addons before the hook runs. Only fires when a hook is set."
        on={state.snapshotBeforeDeploy}
        onChange={(v) => setState((s) => ({ ...s, snapshotBeforeDeploy: v }))}
      />
    </Section>
  );
}
