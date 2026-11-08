"use client";

import { Hammer } from "lucide-react";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { useRegistryCredentials } from "@/features/registry-credentials";
import {
  Section,
  Field,
  FieldGrid,
  Segmented,
  Code,
  RUNTIMES,
  monoInputClass,
  textareaClass,
  type SectionProps,
} from "./_primitives";

export function BuildSection({ state, setState, project }: SectionProps & { project: string }) {
  const isDockerfile = state.runtime === "dockerfile";
  // runtime=image services never build — the chart pulls the image
  // straight from the registry. Editing the reference here + saving
  // IS the redeploy path for them, so instead of the build-strategy
  // pills (which would silently convert the service to a build
  // runtime with no repo configured) we surface the image ref.
  if (state.runtime === "image") {
    return (
      <Section
        id="build"
        title="Image"
        icon={Hammer}
        status="Pre-built image"
        description="This service runs a pre-built image and never builds. Saving a new tag is how you deploy it."
        footerNote="Saving a new repository or tag rolls the service onto that image."
      >
        <Field label="Repository" hint="Full registry path." htmlFor="build-image-repo">
          <Input
            id="build-image-repo"
            value={state.imageRepository}
            onChange={(e) => setState((s) => ({ ...s, imageRepository: e.target.value }))}
            placeholder="e.g. ghcr.io/owner/app"
            className={monoInputClass}
            spellCheck={false}
          />
        </Field>
        <Field label="Tag" hint="Blank uses latest." htmlFor="build-image-tag">
          <Input
            id="build-image-tag"
            value={state.imageTag}
            onChange={(e) => setState((s) => ({ ...s, imageTag: e.target.value }))}
            placeholder="latest"
            className={cn(monoInputClass, "sm:max-w-xs")}
            spellCheck={false}
          />
        </Field>
        <PullSecretRow project={project} state={state} setState={setState} />
      </Section>
    );
  }
  return (
    <Section
      id="build"
      title="Build"
      icon={Hammer}
      status={state.runtime}
      description="How kuso turns the source into an image."
      footerNote="Build changes apply from the next build."
    >
      <Field label="Builder" hint={RUNTIME_HINTS[state.runtime] ?? "How kuso builds the image."}>
        <Segmented
          options={RUNTIMES.map((r) => ({ value: r, label: RUNTIME_LABELS[r] }))}
          value={state.runtime}
          onChange={(r) => setState((s) => ({ ...s, runtime: r }))}
          label="Builder"
        />
      </Field>
      {isDockerfile && (
        <Field label="Dockerfile path" hint="Relative to the root directory. Blank uses Dockerfile." htmlFor="build-dockerfile">
          <Input
            id="build-dockerfile"
            value={state.dockerfile}
            onChange={(e) => setState((s) => ({ ...s, dockerfile: e.target.value }))}
            placeholder="Dockerfile"
            className={cn(monoInputClass, "sm:max-w-xs")}
            spellCheck={false}
          />
        </Field>
      )}
      {state.runtime === "static" && (
        <FieldGrid>
          <TextRow state={state} setState={setState} field="staticBuildCmd" label="Build command" hint="Runs before the site is packaged. Blank runs nothing." placeholder="e.g. npm ci && npm run build" />
          <TextRow state={state} setState={setState} field="staticOutputDir" label="Output directory" hint="Served as the site root." placeholder="." />
          <TextRow state={state} setState={setState} field="staticBuilderImage" label="Builder image" hint="The build command runs in this image." placeholder="node:20-alpine" />
          <TextRow state={state} setState={setState} field="staticRuntimeImage" label="Runtime image" hint="Serves the built files." placeholder="nginx:1.27-alpine" />
        </FieldGrid>
      )}
      {state.runtime === "buildpacks" && (
        <FieldGrid>
          <TextRow state={state} setState={setState} field="buildpacksBuilderImage" label="Builder image" hint="Cloud Native Buildpacks builder. Blank uses Paketo jammy-base." placeholder="paketobuildpacks/builder-jammy-base:latest" />
          <TextRow state={state} setState={setState} field="buildpacksLifecycleImage" label="Lifecycle image" hint="Blank uses the chart default." placeholder="buildpacksio/lifecycle:0.20.5" />
        </FieldGrid>
      )}
      <Field
        label="Build arguments"
        hint={
          <>
            One <Code>KEY=value</Code> per line, passed as <Code>--build-arg</Code> and <Code>ENV</Code>. They end up in
            the image, so keep tokens and passwords in Variables instead.
          </>
        }
        htmlFor="build-args"
      >
        <textarea
          id="build-args"
          value={state.buildArgs}
          onChange={(e) => setState((s) => ({ ...s, buildArgs: e.target.value }))}
          placeholder={"e.g. NODE_ENV=production\nAPP_VERSION=1.2.3"}
          rows={3}
          spellCheck={false}
          className={textareaClass}
        />
      </Field>
      <Field
        label="Public env vars"
        hint={
          <>
            Variable names baked into the client bundle at build time and filled in when the pod starts, e.g.{" "}
            <Code>NEXT_PUBLIC_*</Code>. One per line.
          </>
        }
        htmlFor="build-public-env"
      >
        <textarea
          id="build-public-env"
          value={state.publicEnv}
          onChange={(e) => setState((s) => ({ ...s, publicEnv: e.target.value }))}
          placeholder={"e.g. NEXT_PUBLIC_API_URL\nNEXT_PUBLIC_SITE_URL"}
          rows={2}
          spellCheck={false}
          className={textareaClass}
        />
      </Field>
    </Section>
  );
}

const RUNTIME_LABELS: Record<(typeof RUNTIMES)[number], string> = {
  dockerfile: "Dockerfile",
  nixpacks: "Nixpacks",
  static: "Static site",
  buildpacks: "Buildpacks",
};

const RUNTIME_HINTS: Record<string, string> = {
  dockerfile: "Builds the Dockerfile in your repository.",
  nixpacks: "Detects the language and builds without a Dockerfile.",
  static: "Runs a build command and serves the output with nginx.",
  buildpacks: "Builds with Cloud Native Buildpacks.",
};

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
    <Field label={label} hint={hint} htmlFor={`build-${field}`}>
      <Input
        id={`build-${field}`}
        value={state[field]}
        onChange={(e) => setState((s) => ({ ...s, [field]: e.target.value }))}
        placeholder={placeholder}
        className={monoInputClass}
        spellCheck={false}
      />
    </Field>
  );
}

// PullSecretRow picks the project registry credential a private image is
// pulled with. A segmented control: the list is short and this keeps
// clear of the dropdown-menu primitive.
function PullSecretRow({ project, state, setState }: SectionProps & { project: string }) {
  const creds = useRegistryCredentials(project);
  const options = [
    { value: "", label: "None (public image)" },
    ...(creds.data ?? []).map((c) => ({ value: c.secretName, label: c.registry })),
  ];
  // A credential that was deleted out from under the service still shows,
  // so the user can see and clear it.
  if (state.imagePullSecret && !options.some((o) => o.value === state.imagePullSecret)) {
    options.push({ value: state.imagePullSecret, label: `${state.imagePullSecret} (missing)` });
  }
  return (
    <Field
      label="Registry login"
      hint="For private images. Add logins under project settings."
    >
      <Segmented
        options={options}
        value={state.imagePullSecret}
        onChange={(v) => setState((s) => ({ ...s, imagePullSecret: v }))}
        label="Registry login"
      />
    </Field>
  );
}
