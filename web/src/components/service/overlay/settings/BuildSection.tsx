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
        />
      )}
      {state.runtime === "static" && (
        <>
          <TextRow state={state} setState={setState} field="staticBuildCmd" label="build command" hint="runs before the site is packaged; blank = none" placeholder="npm ci && npm run build" />
          <TextRow state={state} setState={setState} field="staticOutputDir" label="output dir" hint="directory served as the site root" placeholder="." />
          <TextRow state={state} setState={setState} field="staticBuilderImage" label="builder image" hint="image the build command runs in" placeholder="node:20-alpine" />
          <TextRow state={state} setState={setState} field="staticRuntimeImage" label="runtime image" hint="image that serves the files" placeholder="nginx:1.27-alpine" />
        </>
      )}
      {state.runtime === "buildpacks" && (
        <>
          <TextRow state={state} setState={setState} field="buildpacksBuilderImage" label="builder image" hint="CNB builder; blank = Paketo jammy-base" placeholder="paketobuildpacks/builder-jammy-base:latest" />
          <TextRow state={state} setState={setState} field="buildpacksLifecycleImage" label="lifecycle image" hint="CNB lifecycle; blank = chart default" placeholder="buildpacksio/lifecycle:0.20.5" />
        </>
      )}
      <Row
        label="build args"
        hint="KEY=value per line · non-secret build-time constants (passed as --build-arg / ENV) · use secrets for tokens"
        control={
          <textarea
            value={state.buildArgs}
            onChange={(e) => setState((s) => ({ ...s, buildArgs: e.target.value }))}
            placeholder={"NODE_ENV=production\nAPP_VERSION=1.2.3"}
            rows={3}
            spellCheck={false}
            aria-label="Build args"
            className="h-auto w-72 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1 font-mono text-[11px]"
          />
        }
      />
      <Row
        label="public env"
        hint="env-var names baked into the client bundle at build and filled in at pod start (e.g. NEXT_PUBLIC_*) · one per line"
        control={
          <textarea
            value={state.publicEnv}
            onChange={(e) => setState((s) => ({ ...s, publicEnv: e.target.value }))}
            placeholder={"NEXT_PUBLIC_API_URL\nNEXT_PUBLIC_SITE_URL"}
            rows={2}
            spellCheck={false}
            aria-label="Public env"
            className="h-auto w-72 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1 font-mono text-[11px]"
          />
        }
        last
      />
    </Section>
  );
}

type TextField =
  | "staticBuildCmd"
  | "staticOutputDir"
  | "staticBuilderImage"
  | "staticRuntimeImage"
  | "buildpacksBuilderImage"
  | "buildpacksLifecycleImage";

function TextRow({
  state,
  setState,
  field,
  label,
  hint,
  placeholder,
}: SectionProps & { field: TextField; label: string; hint: string; placeholder: string }) {
  return (
    <Row
      label={label}
      hint={hint}
      control={
        <Input
          value={state[field]}
          onChange={(e) => setState((s) => ({ ...s, [field]: e.target.value }))}
          placeholder={placeholder}
          aria-label={label}
          className="h-7 w-full max-w-[320px] font-mono text-[12px]"
          spellCheck={false}
        />
      }
    />
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
