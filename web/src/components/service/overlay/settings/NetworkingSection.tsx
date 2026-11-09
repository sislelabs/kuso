"use client";

import { Network } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Section, Field, FieldGrid, SwitchField, Code, monoInputClass, type SectionProps } from "./_primitives";

// NetworkingSection: how the service is reachable and what it can reach.
// Domains live in their own card (DomainsCard) because they apply
// immediately instead of through Save.
export function NetworkingSection({
  state,
  setState,
  certIssuer,
}: SectionProps & {
  // Whether the cluster can issue certificates; undefined = unknown.
  certIssuer?: boolean;
}) {
  return (
    <Section
      id="networking"
      title="Networking"
      icon={Network}
      status={state.internal ? "Internal only" : "Public"}
      description="The port your app listens on, whether it's on the internet, and what its pods can reach."
      footerNote="Port and egress changes roll the pods. Going internal removes the public URL."
    >
      <Field
        label="Port"
        hint={
          <>
            The container port. kuso sets <Code>PORT</Code> to this, so apps that read it bind correctly.
          </>
        }
        htmlFor="net-port"
      >
        <Input
          id="net-port"
          type="number"
          inputMode="numeric"
          value={state.port}
          onChange={(e) => setState((s) => ({ ...s, port: e.target.value }))}
          min={1}
          max={65535}
          className={`${monoInputClass} w-32`}
        />
      </Field>
      <SwitchField
        label="Internal only"
        hint={
          state.internal
            ? "No public URL. Other services in the project reach it over cluster DNS."
            : certIssuer === true
              ? "Public: served through the ingress with a generated domain and a Let's Encrypt certificate."
              : certIssuer === false
                ? "Public: served through the ingress with a generated domain, over plain HTTP. This cluster has no certificate issuer (cert-manager), so no certificate is requested."
                : "Public: served through the ingress with a generated domain."
        }
        on={state.internal}
        onChange={(v) => setState((s) => ({ ...s, internal: v }))}
      />
      <SwitchField
        label="Private egress"
        hint={
          state.privateEgress
            ? "Pods can only reach this project's services, not the public internet."
            : "Pods can reach the public internet."
        }
        on={state.privateEgress}
        onChange={(v) => setState((s) => ({ ...s, privateEgress: v }))}
      />
      <SwitchField
        label="kuso API access"
        hint="Let pods call the kuso API in-cluster, for apps that orchestrate kuso."
        on={state.platformApiEgress}
        onChange={(v) => setState((s) => ({ ...s, platformApiEgress: v }))}
      />
      {!state.internal && (
        <div className="space-y-4 border-t border-[var(--border-subtle)] pt-5">
          <div>
            <div className="text-[13px] font-medium text-[var(--text-primary)]">Request limits</div>
            <p className="mt-0.5 text-xs leading-relaxed text-[var(--text-secondary)]">
              Per ingress replica. Past a limit the service answers 429. Blank uses the project default.
            </p>
          </div>
          <FieldGrid cols={3}>
            <Field label="Max concurrent" hint="-1 for no cap. Default 1000." htmlFor="net-max-concurrent">
              <Input
                id="net-max-concurrent"
                type="number"
                inputMode="numeric"
                value={state.limitMaxConcurrent}
                onChange={(e) => setState((s) => ({ ...s, limitMaxConcurrent: e.target.value }))}
                placeholder="default"
                min={-1}
                className={monoInputClass}
              />
            </Field>
            <Field label="Requests per second" hint="Blank means no rate limit." htmlFor="net-rate">
              <Input
                id="net-rate"
                type="number"
                inputMode="numeric"
                value={state.limitRate}
                onChange={(e) => setState((s) => ({ ...s, limitRate: e.target.value }))}
                placeholder="none"
                min={-1}
                className={monoInputClass}
              />
            </Field>
            <Field label="Burst" hint="Short spikes above the rate. Blank = rate." htmlFor="net-burst">
              <Input
                id="net-burst"
                type="number"
                inputMode="numeric"
                value={state.limitBurst}
                onChange={(e) => setState((s) => ({ ...s, limitBurst: e.target.value }))}
                placeholder="rate"
                min={0}
                className={monoInputClass}
              />
            </Field>
          </FieldGrid>
        </div>
      )}
    </Section>
  );
}
