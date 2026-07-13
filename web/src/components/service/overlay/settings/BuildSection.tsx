"use client";

import { Hammer } from "lucide-react";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Section, Row, RUNTIMES, type SectionProps } from "./_primitives";

export function BuildSection({ state, setState }: SectionProps) {
  const isDockerfile = state.runtime === "dockerfile";
  // runtime=image services never build — the chart pulls the image
  // straight from the registry. Editing the reference here + saving
  // IS the redeploy path for them, so instead of the build-strategy
  // pills (which would silently convert the service to a build
  // runtime with no repo configured) we surface the image ref.
  if (state.runtime === "image") {
    return (
      <Section id="build" title="Image" icon={Hammer} hint="pre-built — no build pipeline">
        <Row
          label="repository"
          hint="full registry path, e.g. ghcr.io/owner/app"
          control={
            <Input
              value={state.imageRepository}
              onChange={(e) => setState((s) => ({ ...s, imageRepository: e.target.value }))}
              placeholder="ghcr.io/owner/app"
              className="h-7 w-full font-mono text-[12px]"
              spellCheck={false}
            />
          }
        />
        <Row
          label="tag"
          hint="save a new tag to roll the service; blank = latest"
          control={
            <Input
              value={state.imageTag}
              onChange={(e) => setState((s) => ({ ...s, imageTag: e.target.value }))}
              placeholder="latest"
              className="h-7 w-48 font-mono text-[12px]"
              spellCheck={false}
            />
          }
          last
        />
      </Section>
    );
  }
  return (
    <Section id="build" title="Build" icon={Hammer}>
      <Row
        label="strategy"
        hint="how kuso builds the image"
        control={
          // Pills are nowrap + tighter (px-1.5, text-[10px]) so the
          // four strategies fit on one line at typical overlay
          // widths. The wrap-fallback is still there for very
          // narrow viewports but is rare in practice now.
          <div className="inline-flex flex-nowrap items-center gap-0.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
            {RUNTIMES.map((r) => (
              <button
                key={r}
                type="button"
                onClick={() => setState((s) => ({ ...s, runtime: r }))}
                className={cn(
                  "rounded px-1.5 py-1 font-mono text-[10px] whitespace-nowrap transition-colors",
                  state.runtime === r
                    ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                    : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
                )}
              >
                {r}
              </button>
            ))}
          </div>
        }
        last={!isDockerfile}
      />
      {isDockerfile && (
        <Row
          label="dockerfile"
          hint="path to Dockerfile (relative to source path); blank = Dockerfile"
          control={
            <Input
              value={state.dockerfile}
              onChange={(e) => setState((s) => ({ ...s, dockerfile: e.target.value }))}
              placeholder="Dockerfile"
              className="h-7 w-48 font-mono text-[12px]"
              spellCheck={false}
            />
          }
          last
        />
      )}
    </Section>
  );
}
