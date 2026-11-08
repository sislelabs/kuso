"use client";

import { HeartPulse } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Section, Field, FieldGrid, SwitchField, WithSuffix, Callout, Code, monoInputClass, type SectionProps } from "./_primitives";
import { HC_DEFAULTS, secondsUntilRestart } from "./healthcheckPatch";
import { HealthcheckTestButton, HealthcheckTestResultView, useHealthcheckTestRunner } from "./HealthcheckTest";

// HealthcheckSection edits spec.healthcheck. Off = the chart's default TCP
// readiness check (the port accepts connections) with no liveness probe.
// On = HTTP readiness AND liveness against the path, so a process that
// still accepts connections but has stopped answering gets restarted.
export function HealthcheckSection({
  state,
  setState,
  project,
  service,
  env,
}: SectionProps & { project: string; service: string; env: string }) {
  const on = state.hcEnabled;
  const tester = useHealthcheckTestRunner({ project, service, env, path: state.hcPath, port: state.hcPort || state.port });
  const set = (patch: Partial<typeof state>) => setState((s) => ({ ...s, ...patch }));
  const restartAfter = secondsUntilRestart(state);
  const num = (id: string, value: string, key: "hcDelay" | "hcPeriod" | "hcTimeout" | "hcThreshold", placeholder: number, suffix: string) => (
    <WithSuffix suffix={suffix}>
      <Input
        id={id}
        type="number"
        inputMode="numeric"
        min={1}
        value={value}
        onChange={(e) => {
          const v = e.target.value;
          setState((s) => ({ ...s, [key]: v }));
        }}
        placeholder={String(placeholder)}
        className={monoInputClass}
      />
    </WithSuffix>
  );

  return (
    <Section
      id="healthcheck"
      title="Health check"
      icon={HeartPulse}
      status={on && state.hcPath.trim() ? `HTTP ${state.hcPath.trim()}` : "Port check"}
      description="How kuso decides a pod is ready for traffic and when to restart one that stopped responding."
      footerNote="Saving restarts every pod of this service with the new check."
    >
      <SwitchField
        label="HTTP health check"
        hint={
          on ? (
            <>
              kuso sends <Code>GET {state.hcPath.trim() || "/health"}</Code> to each pod. A 2xx or 3xx answer means
              healthy.
            </>
          ) : (
            "Off: a pod is ready as soon as it accepts connections on its port, and it's never restarted for being unresponsive."
          )
        }
        on={on}
        onChange={(v) => set({ hcEnabled: v, ...(v && !state.hcPath.trim() ? { hcPath: "/health" } : {}) })}
      />
      {on && (
        <>
          <FieldGrid>
            <Field label="Path" hint="Must answer without authentication." htmlFor="hc-path">
              <div className="flex items-center gap-2">
                <Input
                  id="hc-path"
                  value={state.hcPath}
                  onChange={(e) => set({ hcPath: e.target.value })}
                  placeholder="/health"
                  spellCheck={false}
                  className={monoInputClass}
                />
                <HealthcheckTestButton runner={tester} />
              </div>
            </Field>
            <Field label="Port" hint="Blank uses the service port." htmlFor="hc-port">
              <Input
                id="hc-port"
                type="number"
                inputMode="numeric"
                min={1}
                max={65535}
                value={state.hcPort}
                onChange={(e) => set({ hcPort: e.target.value })}
                placeholder={state.port || "service port"}
                className={monoInputClass}
              />
            </Field>
          </FieldGrid>
          <HealthcheckTestResultView runner={tester} />
          <FieldGrid>
            <Field
              label="Initial delay"
              hint="Wait this long after the app starts listening before the first check."
              htmlFor="hc-delay"
            >
              {num("hc-delay", state.hcDelay, "hcDelay", HC_DEFAULTS.delay, "seconds")}
            </Field>
            <Field label="Check interval" hint="How often each pod is checked." htmlFor="hc-period">
              {num("hc-period", state.hcPeriod, "hcPeriod", HC_DEFAULTS.period, "seconds")}
            </Field>
            <Field label="Timeout" hint="A slower answer counts as a failure." htmlFor="hc-timeout">
              {num("hc-timeout", state.hcTimeout, "hcTimeout", HC_DEFAULTS.timeout, "seconds")}
            </Field>
            <Field
              label="Failure threshold"
              hint="Failed checks in a row before the pod is pulled from traffic and restarted."
              htmlFor="hc-threshold"
            >
              {num("hc-threshold", state.hcThreshold, "hcThreshold", HC_DEFAULTS.threshold, "failures")}
            </Field>
          </FieldGrid>
          <p className="text-xs leading-relaxed text-[var(--text-secondary)]">
            Blank fields use the defaults. With these values a pod that stops answering is taken out of traffic and
            restarted after about {restartAfter} seconds. Slow-booting apps are safe: the check only starts once the
            port is open.
          </p>
          <Callout tone="warning">
            If the path doesn&apos;t answer, new pods never become ready: the deploy stalls and the previous version
            keeps serving. Check the path responds in your app before saving.
          </Callout>
        </>
      )}
    </Section>
  );
}
