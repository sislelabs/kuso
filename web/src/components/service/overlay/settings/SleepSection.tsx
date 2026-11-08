"use client";

import { Moon } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Section, Field, SwitchField, WithSuffix, monoInputClass, textareaClass, type SectionProps } from "./_primitives";

// SleepSection edits scale-to-zero. sleep.enabled governs the production
// env only; every non-production env (named envs, env-group clones, PR
// previews) sleeps by default unless the service sets
// sleep.nonProduction=off.
export function SleepSection({ state, setState }: SectionProps) {
  const minZero = Number(state.scaleMin) === 0;
  const prodOn = minZero || state.sleepEnabled;
  const keptWarm = state.sleepExcludePaths.trim() !== "";
  const nonProdOn = state.sleepNonProduction !== "off";
  const status = keptWarm
    ? "Kept warm by paths"
    : prodOn
      ? `Sleeps after ${state.sleepAfter || "30"} min`
      : "Production always on";
  return (
    <Section
      id="sleep"
      title="Sleep"
      icon={Moon}
      status={status}
      description="Scale idle environments to zero. The next request wakes them, with a cold start of a few seconds."
    >
      <SwitchField
        label="Sleep production when idle"
        hint={
          minZero
            ? "Min replicas is 0 (Scale), so production always sleeps when idle."
            : "Off keeps production running even with no traffic."
        }
        on={prodOn}
        disabled={minZero}
        stateLabel={prodOn ? "Sleeps" : "Always on"}
        onChange={() => setState((s) => ({ ...s, sleepEnabled: !s.sleepEnabled }))}
      />
      <SwitchField
        label="Sleep other environments when idle"
        hint="Named environments, env-group clones and PR previews."
        on={nonProdOn}
        stateLabel={nonProdOn ? "Sleeps" : "Always on"}
        onChange={() =>
          setState((s) => ({ ...s, sleepNonProduction: s.sleepNonProduction === "off" ? "on" : "off" }))
        }
      />
      <Field
        label="Idle window"
        hint="Minutes without a request before an environment sleeps. Applies to production and the others."
        htmlFor="sleep-after"
      >
        <WithSuffix suffix="minutes">
          <Input
            id="sleep-after"
            type="number"
            inputMode="numeric"
            value={state.sleepAfter}
            onChange={(e) => setState((s) => ({ ...s, sleepAfter: e.target.value }))}
            className={monoInputClass}
            min={1}
          />
        </WithSuffix>
      </Field>
      {/* Any request to a listed path keeps the WHOLE deployment warm,
          so a webhook/callback doesn't hit a cold start. */}
      <Field
        label="Keep-warm paths"
        hint="One path per line, e.g. webhook endpoints that can't wait for a cold start. While any are set, this service never sleeps."
        htmlFor="sleep-keep-warm"
      >
        <textarea
          id="sleep-keep-warm"
          value={state.sleepExcludePaths}
          onChange={(e) => setState((s) => ({ ...s, sleepExcludePaths: e.target.value }))}
          placeholder={"e.g. /api/webhooks/stripe\n/api/callbacks/github"}
          rows={3}
          spellCheck={false}
          className={textareaClass}
        />
      </Field>
    </Section>
  );
}
