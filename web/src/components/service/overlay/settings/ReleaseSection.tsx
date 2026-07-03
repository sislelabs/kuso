"use client";

import { Rocket } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Section, Row, type SectionProps } from "./_primitives";

// ReleaseSection configures the pre-deploy release hook: a one-off Job
// that runs before a rollout is promoted (migrations, etc). Empty
// command = no hook (the default for every service).
export function ReleaseSection({ state, setState }: SectionProps) {
  const hasHook = state.releaseCommand.trim().length > 0;
  const hint = hasHook ? "runs before each deploy" : "no hook";

  return (
    <Section id="release" title="Release hook" icon={Rocket} hint={hint}>
      <div className="px-3 py-2.5 text-[11px] text-[var(--text-secondary)]">
        This command runs as a one-off Job with the new image and the
        service&apos;s env, before the rollout is promoted. A non-zero exit
        fails the deploy and keeps the old version serving. Empty command =
        no hook.
      </div>
      <Row
        label="command"
        hint='e.g. "npm run migrate" or "bundle exec rake db:migrate"'
        control={
          <Input
            value={state.releaseCommand}
            onChange={(e) => setState((s) => ({ ...s, releaseCommand: e.target.value }))}
            placeholder="e.g. npm run migrate"
            className="h-7 w-full font-mono text-[12px]"
          />
        }
      />
      <Row
        label="timeout"
        hint="seconds — blank = server default (900)"
        control={
          <Input
            type="number"
            value={state.releaseTimeout}
            onChange={(e) => setState((s) => ({ ...s, releaseTimeout: e.target.value }))}
            placeholder="900"
            className="h-7 w-24 font-mono text-[12px]"
            min={1}
          />
        }
        last
      />
    </Section>
  );
}
