"use client";

import { useMemo, useRef, useState, type ReactNode } from "react";
import { Rocket } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { parseImageRef, type ImageRef } from "@/features/services/imageRef";
import { projectNameError, projectSlugFromRepo } from "@/features/projects/names";
import { defaultServiceHost } from "@/lib/default-host";
import { FormField } from "../[project]/services/new/FormField";
import { EnvPastePanel, useDotenv } from "../[project]/services/new/EnvPastePanel";

export interface DeployImageInput {
  projectName: string;
  image: ImageRef;
  port: string;
  envVars: { name: string; value: string }[];
}

const monoInputClass = "h-9 font-mono text-[13px]";

// DeployImageForm is the new-project path for a pre-built container
// image: no repo, no build. It creates the project and one image service
// (the add-service image path) named after the image.
export function DeployImageForm({
  taken,
  instanceDomain,
  baseDomain,
  advanced,
  formError,
  submitting,
  onSubmit,
}: {
  taken: ReadonlySet<string>;
  instanceDomain: string;
  baseDomain: string;
  // The page's shared Advanced fields (base domain, namespace, previews).
  advanced: (projectName: string) => ReactNode;
  formError: string | null;
  submitting: boolean;
  onSubmit: (input: DeployImageInput) => void;
}) {
  const [imageText, setImageText] = useState("");
  const [projectName, setProjectName] = useState("");
  const [nameTouched, setNameTouched] = useState(false);
  const [port, setPort] = useState("");
  const [envText, setEnvText] = useState("");
  const env = useDotenv(envText);
  const [errors, setErrors] = useState<{ image?: string; project?: string; env?: string }>({});
  const imageRef = useRef<HTMLInputElement>(null);
  const nameRef = useRef<HTMLInputElement>(null);

  const parsed = useMemo(() => parseImageRef(imageText), [imageText]);
  const derivedName = parsed ? projectSlugFromRepo(parsed.name, taken) : "";
  const name = nameTouched ? projectName : derivedName;
  const nameProblem = (n: string) =>
    projectNameError(n) ?? (taken.has(n) ? `A project named "${n}" already exists.` : null);
  const host = defaultServiceHost(name.trim() || "my-app", name.trim() || "my-app", baseDomain, instanceDomain);

  const submit = () => {
    const errs = {
      image: !imageText.trim()
        ? "Paste the image to run, e.g. ghcr.io/acme/api:1.4."
        : !parsed
          ? "That isn't an image reference kuso can pull. Use registry/owner/name:tag."
          : undefined,
      project: nameProblem(name.trim()) ?? undefined,
      env: env.errors.length > 0 ? "Fix or remove the .env lines that can't be read." : undefined,
    };
    setErrors(errs);
    if (errs.image) return imageRef.current?.focus();
    if (errs.project) return nameRef.current?.focus();
    if (errs.env || !parsed) return;
    onSubmit({ projectName: name.trim(), image: parsed, port, envVars: env.vars });
  };

  return (
    <div className="space-y-5">
      <FormField
        label="Image"
        hint={
          parsed ? (
            <>
              Pulls <span className="font-mono text-[var(--text-primary)]">{parsed.repository}</span> at{" "}
              <span className="font-mono text-[var(--text-primary)]">{parsed.tag || "latest"}</span>. Public images
              only here; for a private registry, create an empty project and add its registry login first.
            </>
          ) : (
            "Registry path with an optional tag or digest. Immutable tags roll out predictably; latest doesn't."
          )
        }
        error={errors.image}
        htmlFor="image-ref"
      >
        <Input
          id="image-ref"
          ref={imageRef}
          value={imageText}
          onChange={(e) => {
            setImageText(e.target.value);
            setErrors((x) => ({ ...x, image: undefined }));
          }}
          placeholder="e.g. ghcr.io/acme/api:1.4.2"
          aria-invalid={errors.image ? true : undefined}
          spellCheck={false}
          autoComplete="off"
          autoFocus
          className={monoInputClass}
        />
      </FormField>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-[1fr_8rem]">
        <FormField
          label="Project name"
          hint={
            <>
              Your app will be at <span className="font-mono text-[var(--text-primary)]">https://{host}</span>
            </>
          }
          error={errors.project ?? (nameTouched && name ? nameProblem(name.trim()) : null)}
          htmlFor="image-project-name"
        >
          <Input
            id="image-project-name"
            ref={nameRef}
            value={name}
            onChange={(e) => {
              setProjectName(e.target.value);
              setNameTouched(true);
              setErrors((x) => ({ ...x, project: undefined }));
            }}
            placeholder="e.g. my-app"
            spellCheck={false}
            autoComplete="off"
            className={monoInputClass}
          />
        </FormField>
        <FormField label="Port" hint="Defaults to 8080." htmlFor="image-port">
          <Input
            id="image-port"
            type="number"
            inputMode="numeric"
            value={port}
            onChange={(e) => setPort(e.target.value)}
            placeholder="e.g. 3000"
            className={monoInputClass}
          />
        </FormField>
      </div>

      <FormField
        label="Environment variables"
        optional
        hint="Paste a .env; secrets are stored encrypted."
        error={errors.env}
        htmlFor="image-env"
      >
        <EnvPastePanel
          id="image-env"
          text={envText}
          onChange={(t) => {
            setEnvText(t);
            setErrors((x) => ({ ...x, env: undefined }));
          }}
          parsed={env}
        />
      </FormField>

      {advanced(name.trim())}

      {formError && (
        <p role="alert" className="text-xs text-[var(--error)]">
          {formError}
        </p>
      )}
      <div className="flex flex-col gap-3 border-t border-[var(--border-subtle)] pt-4 sm:flex-row sm:items-center sm:justify-between">
        <p className="min-w-0 text-xs text-[var(--text-secondary)]">
          Creates the project and one service that runs this image. No build: it starts as soon as the image is pulled.
        </p>
        <Button type="button" onClick={submit} disabled={submitting} className="h-9 shrink-0 px-4">
          <Rocket className="h-4 w-4" />
          {submitting ? "Creating…" : "Create & deploy"}
        </Button>
      </div>
    </div>
  );
}
