"use client";

import { Hammer } from "lucide-react";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { useRegistryCredentials } from "@/features/registry-credentials";
import { Section, Row, RUNTIMES, type SectionProps } from "./_primitives";

export function BuildSection({ state, setState, project }: SectionProps & { project: string }) {
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
        />
        <PullSecretRow project={project} state={state} setState={setState} />
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

// PullSecretRow picks the project registry credential a private image is
// pulled with. Pills, not a dropdown: the list is short and this keeps
// clear of the dropdown-menu primitive.
function PullSecretRow({ project, state, setState }: SectionProps & { project: string }) {
  const creds = useRegistryCredentials(project);
  const options = [
    { value: "", label: "none (public)" },
    ...(creds.data ?? []).map((c) => ({ value: c.secretName, label: c.registry })),
  ];
  // A credential that was deleted out from under the service still shows,
  // so the user can see and clear it.
  if (state.imagePullSecret && !options.some((o) => o.value === state.imagePullSecret)) {
    options.push({ value: state.imagePullSecret, label: `${state.imagePullSecret} (missing)` });
  }
  return (
    <Row
      label="registry login"
      hint="private images — add logins under project settings"
      control={
        <div className="inline-flex flex-wrap items-center gap-0.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
          {options.map((o) => (
            <button
              key={o.value || "none"}
              type="button"
              onClick={() => setState((s) => ({ ...s, imagePullSecret: o.value }))}
              className={cn(
                "rounded px-1.5 py-1 font-mono text-[10px] whitespace-nowrap transition-colors",
                state.imagePullSecret === o.value
                  ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                  : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
              )}
            >
              {o.label}
            </button>
          ))}
        </div>
      }
      last
    />
  );
}
