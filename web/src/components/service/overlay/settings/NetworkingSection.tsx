"use client";

import { Network } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Section, Row, type SectionProps } from "./_primitives";

export function NetworkingSection({ state, setState }: SectionProps) {
  return (
    <Section id="networking" title="Networking" icon={Network}>
      <Row
        label="port"
        hint="container port"
        control={
          <Input
            type="number"
            value={state.port}
            onChange={(e) => setState((s) => ({ ...s, port: e.target.value }))}
            min={1}
            max={65535}
            className="h-7 w-24 font-mono text-[12px]"
          />
        }
      />
      <Row
        label="domains"
        hint="auto-TLS; one per line"
        control={
          <textarea
            value={state.domains}
            onChange={(e) => setState((s) => ({ ...s, domains: e.target.value }))}
            spellCheck={false}
            placeholder="api.example.com"
            rows={Math.max(1, Math.min(4, state.domains.split("\n").length))}
            className="w-full max-w-[320px] resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1 font-mono text-[12px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
          />
        }
        last
      />
    </Section>
  );
}
