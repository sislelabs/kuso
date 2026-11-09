"use client";

import { PageHeader } from "@/components/shared/PageHeader";
import { useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/select";
import { Github, Check, Plus, Box, Rocket, GitBranch, Loader2, SlidersHorizontal } from "lucide-react";
import { toast } from "sonner";
import type { AddonSuggestion } from "@/features/github";
import { useRouteParams } from "@/lib/dynamic-params";
import { addAddon, projectQueryKey, useAddons, useProject, useServices } from "@/features/projects";
import { SERVICE_DISPLAY_NAME_RE, friendlyApiError, serviceSlugError } from "@/features/projects/names";
import { useRegistryCredentials } from "@/features/registry-credentials";
import { addonLabel } from "@/components/addon/AddonIcon";
import { SIZE_NONE, sizingBody, type SizingInput } from "@/features/services/createSizing";
import { buildCreateServiceBody } from "@/features/services/createServiceBody";
import { createServiceWithEnv } from "@/features/services/createServiceFlow";
import { listPodSizes } from "@/features/cluster-config/api";
import { ApiError } from "@/lib/api-client";
import { serviceShortName } from "@/lib/utils";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { slugifyServiceName } from "@/features/services/slug";
import { imageRefString, imageRepositoryProblem, imageTagProblem, specImageTag, splitPastedImage } from "@/features/services/imageRef";
import { FormField, SegmentedControl } from "./FormField";
import { RepoPicker, pickedRepoURL, useRepoSources, type PickedRepo } from "./RepoPicker";
import { useRepoDetection } from "./useRepoDetection";
import { detectionCopy, type DetectionCopy, type Provenance } from "./detectionCopy";
import { AccessCheck, RepoReadFailureCallout, RuntimeExplainer } from "./RepoInsights";
import { EnvPastePanel, useDotenv } from "./EnvPastePanel";
import { reportCreateOutcome } from "./reportCreateOutcome";
import { servicePageHref } from "@/components/service/workspace/hrefs";

// Addon kinds a scan suggestion can turn into with one click — the
// scanner also reports kinds kuso has no chart for (kafka, memcached…).
const SUGGESTABLE_ADDON_KINDS = new Set(["postgres", "mysql", "mongodb", "redis", "rabbitmq", "clickhouse"]);

const RUNTIMES = [
  { value: "dockerfile", label: "Dockerfile", hint: "build the repo's Dockerfile" },
  { value: "nixpacks", label: "Nixpacks", hint: "auto-detect the language and build without a Dockerfile" },
  { value: "static", label: "Static", hint: "serve built files (e.g. index.html) from a web server" },
  { value: "buildpacks", label: "Buildpacks", hint: "Cloud Native Buildpacks (Paketo)" },
] as const;

const inputClass = "h-9 text-[13px]";
const monoInputClass = "h-9 font-mono text-[13px]";

// imageRegistryHost mirrors registrycreds.NormalizeRegistry's view of
// which registry an image reference points at.
export function imageRegistryHost(image: string): string {
  const ref = image.trim().toLowerCase();
  const slash = ref.indexOf("/");
  if (slash > 0) {
    const first = ref.slice(0, slash);
    if (first.includes(".") || first.includes(":") || first === "localhost") {
      return first === "index.docker.io" || first === "registry-1.docker.io" ? "docker.io" : first;
    }
  }
  return "docker.io";
}

// AddServiceView is the per-project add-service flow, in four steps:
// source (repo or image) → configure → variables → deploy. One service
// per run; monorepos repeat the flow with a different path.
export function AddServiceView() {
  const router = useRouter();
  const qc = useQueryClient();
  const params = useRouteParams<{ project: string }>(["project"]);
  const project = params.project ?? "";

  const repoSources = useRepoSources();
  const projectAddons = useAddons(project);
  const registryCreds = useRegistryCredentials(project);

  // "repo" builds from git; "image" deploys a pre-built OCI image (no
  // build, no GitHub App), so an install without the App can still get
  // to first compute.
  const [source, setSource] = useState<"repo" | "image">("repo");
  const [imageRepo, setImageRepo] = useState("");
  const [imageTag, setImageTag] = useState("");
  // Registry credential secretName for image.pullSecret; "" = public
  // pull. `null` means "follow the image host" until the user picks.
  const [pullSecretChoice, setPullSecretChoice] = useState<string | null>(null);
  const autoPullSecret = useMemo(() => {
    if (!imageRepo.trim()) return "";
    const host = imageRegistryHost(imageRepo);
    return (registryCreds.data ?? []).find((c) => c.registry === host)?.secretName ?? "";
  }, [imageRepo, registryCreds.data]);
  const pullSecret = pullSecretChoice ?? autoPullSecret;

  const [picked, setPicked] = useState<PickedRepo | null>(null);
  // Display name is free-form ("Todo API"); the URL slug derives from it.
  // Renaming the slug later clones kube resources, so it's shown here.
  const [name, setName] = useState("");
  const slug = useMemo(() => slugifyServiceName(name), [name]);
  const [path, setPath] = useState("");
  // "" until detection lands (or the user picks one).
  const [runtime, setRuntime] = useState<string>("");
  // A worker isn't a build runtime: it reuses a sibling's image with a
  // different command.
  const [kind, setKind] = useState<"web" | "worker">("web");
  const [addonPicks, setAddonPicks] = useState<Set<string>>(new Set());
  const [dockerfile, setDockerfile] = useState<string>("");
  const [command, setCommand] = useState<string>("");
  const [fromService, setFromService] = useState<string>("");
  const [port, setPort] = useState<string>("");
  const [envText, setEnvText] = useState("");
  const env = useDotenv(envText);
  const [submitting, setSubmitting] = useState(false);
  // Repo services start on the one-card "deploy with detected settings"
  // path; the full form opens on Edit (or on a validation error).
  const [advanced, setAdvanced] = useState(false);

  const detection = useRepoDetection(picked, path, (rt, p) => {
    setRuntime(rt);
    if (p) setPort(String(p));
    clearFieldError("runtime");
  });

  const [fieldErrors, setFieldErrors] = useState<{
    name?: string;
    image?: string;
    tag?: string;
    repo?: string;
    runtime?: string;
    fromService?: string;
    sizing?: string;
    env?: string;
  }>({});
  const [sizing, setSizing] = useState<SizingInput>({ size: "", replicas: "", maxReplicas: "" });
  // Same cache key as the service settings Scale section.
  const podSizes = useQuery({ queryKey: ["admin", "podsizes"], queryFn: listPodSizes });
  const clearFieldError = (key: keyof typeof fieldErrors) =>
    setFieldErrors((prev) => (prev[key] ? { ...prev, [key]: undefined } : prev));
  // The two name inputs (image vs repo mode) are exclusive, so one ref.
  const nameInputRef = useRef<HTMLInputElement>(null);
  const imageInputRef = useRef<HTMLInputElement>(null);
  const tagInputRef = useRef<HTMLInputElement>(null);
  // A full reference typed or pasted into the repository field moves its
  // tag/digest into the Tag field.
  const splitImageInput = (raw: string): boolean => {
    const split = splitPastedImage(raw);
    if (!split) return false;
    setImageRepo(split.repository);
    setImageTag(split.tag);
    setFieldErrors((prev) => ({ ...prev, image: undefined, tag: undefined }));
    return true;
  };
  const repoSearchRef = useRef<HTMLInputElement>(null);
  const envRef = useRef<HTMLDivElement>(null);
  const runtimeRef = useRef<HTMLDivElement>(null);

  // Worker image sources: the project's services minus workers and
  // image services (no builds, so their tag never propagates).
  const projectServices = useServices(project);
  const workerSources = useMemo(
    () =>
      (projectServices.data ?? [])
        .filter((s) => s.spec.runtime !== "worker" && s.spec.runtime !== "image")
        .map((s) => serviceShortName(project, s.metadata.name)),
    [projectServices.data, project],
  );

  // Preselect the project's default repository (if the GitHub App can
  // see it) once, so "Add service" on a repo-backed project starts one
  // click shorter. A ref guard keeps "change" from re-picking it.
  const projectQuery = useProject(project);
  const projectRepo = projectQuery.data?.project?.spec?.defaultRepo ?? null;
  const defaultRepoURL = projectRepo?.url ?? "";
  const defaultRepoApplied = useRef(false);
  useEffect(() => {
    if (defaultRepoApplied.current || picked || !defaultRepoURL || repoSources.allRepos.length === 0) return;
    defaultRepoApplied.current = true;
    const full = defaultRepoURL
      .replace(/^https?:\/\/github\.com\//, "")
      .replace(/\/+$/, "")
      .replace(/\.git$/, "")
      .toLowerCase();
    const match = repoSources.allRepos.find(({ repo }) => repo.fullName.toLowerCase() === full);
    if (match) pick({ installationId: match.installationId, repo: match.repo });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [defaultRepoURL, repoSources.allRepos, picked]);

  const pick = (p: PickedRepo) => {
    setPicked(p);
    clearFieldError("repo");
    // Repo names are usually kebab-case already; swap "_" / "." which the
    // display-name rule rejects so an untouched prefill always submits.
    if (!name) setName((p.repo.fullName.split("/").pop() || "service").replace(/[^A-Za-z0-9 -]+/g, "-").slice(0, 60));
  };

  const unpick = () => {
    setPicked(null);
    setAdvanced(false);
    setName("");
    setPath("");
    setRuntime("");
    setAddonPicks(new Set());
  };

  const addonSuggestions = useMemo((): AddonSuggestion[] => {
    const have = new Set((projectAddons.data ?? []).map((a) => a.spec.kind));
    return detection.suggestions.filter((s) => SUGGESTABLE_ADDON_KINDS.has(s.kind) && !have.has(s.kind));
  }, [detection.suggestions, projectAddons.data]);

  const toggleAddonPick = (k: string) =>
    setAddonPicks((prev) => {
      const next = new Set(prev);
      if (next.has(k)) next.delete(k);
      else next.add(k);
      return next;
    });

  const effectiveRuntime = kind === "worker" ? "worker" : runtime;
  const willBuild = source === "repo" && kind === "web";
  const showConfigure = source === "image" || !!picked;
  const fastPath = source === "repo" && !!picked && kind === "web" && !advanced;
  const quickCopy = detectionCopy({
    state: detection.state,
    result: detection.result,
    failure: detection.failure,
    runtime,
    port,
    where: repoWhere(path),
  });

  // Detection failed: open the full form and land on the runtime picker.
  const chooseRuntimeManually = () => {
    setAdvanced(true);
    requestAnimationFrame(() => {
      runtimeRef.current?.scrollIntoView({ block: "center", behavior: "smooth" });
      runtimeRef.current?.querySelector<HTMLButtonElement>("button[aria-pressed=true], button")?.focus({ preventScroll: true });
    });
  };

  const onAdd = async (deploy = true) => {
    // Inline per-field errors + focus the first invalid field in visual
    // order. Toasts are for server-side failures only.
    const errs: typeof fieldErrors = {};
    if (source === "repo" && !picked) errs.repo = "Pick a repository or paste its URL to continue.";
    if (!name.trim()) errs.name = "Service name is required.";
    else if (!slug) errs.name = "Name needs at least one letter or digit.";
    else if (!SERVICE_DISPLAY_NAME_RE.test(name.trim())) errs.name = "Use letters, digits, spaces and dashes only (max 60).";
    else {
      const tooLong = serviceSlugError(project, slug);
      if (tooLong) errs.name = tooLong;
    }
    if (source === "image") {
      errs.image = imageRepositoryProblem(imageRepo) ?? undefined;
      errs.tag = imageTagProblem(imageTag) ?? undefined;
    }
    if (source === "repo" && kind === "worker" && !fromService) errs.fromService = "Pick the service whose image this worker runs.";
    if (source === "repo" && picked && kind === "web" && !runtime) errs.runtime = "Pick a runtime.";
    const sized = sizingBody(sizing, podSizes.data ?? []);
    if ("error" in sized) errs.sizing = sized.error;
    if (env.errors.length > 0) {
      errs.env = `${env.errors.length === 1 ? "A line" : `${env.errors.length} lines`} in the .env can't be read; fix or remove ${env.errors.length === 1 ? "it" : "them"}.`;
    }
    setFieldErrors(errs);
    if (Object.values(errs).some(Boolean)) {
      // The quick path hides the fields; open them so the error is visible.
      if (!errs.repo) setAdvanced(true);
      requestAnimationFrame(() => {
        if (errs.repo) repoSearchRef.current?.focus();
        else if (errs.name) nameInputRef.current?.focus();
        else if (errs.image) imageInputRef.current?.focus();
        else if (errs.tag) tagInputRef.current?.focus();
        else if (errs.env) envRef.current?.querySelector("textarea")?.focus();
      });
      return;
    }
    if ("error" in sized) return;

    const body =
      source === "image"
        ? buildCreateServiceBody({ source, name, image: imageRepo.trim(), tag: specImageTag(imageTag), pullSecret, port, sizing: sized })
        : buildCreateServiceBody({
            source,
            name,
            repo: {
              url: pickedRepoURL(picked!),
              branch: picked!.repo.defaultBranch,
              path,
              token: picked!.byUrl?.token,
              installationId: picked!.byUrl ? 0 : picked!.installationId,
            },
            projectRepo,
            kind,
            runtime,
            dockerfile,
            command,
            fromService,
            port,
            sizing: sized,
          });

    setSubmitting(true);
    try {
      const out = await createServiceWithEnv(project, body, {
        envVars: env.vars,
        deploy,
        // Addons come after the service so a rejected service POST can't
        // leave a database behind. A service with no explicit subscription
        // list mounts every project addon, so it still gets DATABASE_URL.
        beforeBuild: async () => {
          const failed: string[] = [];
          for (const addonKind of source === "repo" ? addonPicks : []) {
            if (!addonSuggestions.some((s) => s.kind === addonKind)) continue;
            try {
              await addAddon(project, { name: addonKind, kind: addonKind });
            } catch (ae) {
              if (!(ae instanceof ApiError && ae.status === 409)) failed.push(`${addonKind} (${friendlyApiError(ae, "failed")})`);
            }
          }
          if (failed.length > 0) {
            toast.warning(`Service created, but these addons weren't: ${failed.join(", ")}. Add them from the canvas.`, {
              duration: Infinity,
              closeButton: true,
            });
          }
        },
      });
      // The canvas reads the cached project describe; without this a
      // first service can land on the "Empty project" state.
      await qc.invalidateQueries({ queryKey: projectQueryKey(project) });
      reportCreateOutcome(out, `Service ${name.trim()}`, willBuild ? picked?.repo.defaultBranch : undefined);
      const tab = out.envFailed.length > 0 ? "variables" : "deployments";
      router.replace(servicePageHref(project, body.name, { tab }));
    } catch (e) {
      toast.error(friendlyApiError(e, "Failed to add service"));
    } finally {
      setSubmitting(false);
    }
  };

  const submitLabel = submitting ? "Creating…" : kind === "worker" && source === "repo" ? "Create worker" : "Create & deploy";
  const submitDisabled =
    submitting ||
    (willBuild && detection.state === "detecting") ||
    // The server hard-rejects a worker with no fromService.
    (source === "repo" && kind === "worker" && !fromService);

  return (
    <div className="mx-auto max-w-2xl px-4 py-6 sm:px-6 lg:py-8">
      <PageHeader
        className="mb-6 sm:mb-6"
        breadcrumb={[
          { label: "Projects", href: "/projects" },
          { label: project, href: `/projects/${encodeURIComponent(project)}` },
          { label: "Add service" },
        ]}
        title="Add service"
        description="kuso detects how to build it, you check the settings, then it deploys."
      />

      <ol className="space-y-0">
        <Step n={1} title="Source" description="Where the code or image comes from." done={showConfigure}>
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            <SourceCard
              active={source === "repo"}
              onClick={() => {
                setSource("repo");
                setFieldErrors({});
              }}
              icon={<Github className="h-4 w-4" />}
              title="Git repository"
              hint="kuso builds it, and rebuilds on every push"
            />
            <SourceCard
              active={source === "image"}
              onClick={() => {
                setSource("image");
                setFieldErrors({});
              }}
              icon={<Box className="h-4 w-4" />}
              title="Pre-built image"
              hint="your CI publishes an image; kuso runs it as-is"
            />
          </div>

          {source === "repo" ? (
            <div className="mt-4">
              {picked ? (
                <div className="space-y-3">
                  <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-[var(--accent)]/40 bg-[var(--accent-subtle)] px-3 py-2">
                    <span className="flex min-w-0 items-center gap-2 text-[13px]">
                      <Check className="h-3.5 w-3.5 shrink-0 text-[var(--accent)]" />
                      <span className="truncate font-mono">{picked.repo.fullName}</span>
                    </span>
                    <span className="flex items-center gap-3">
                      <span className="flex items-center gap-1 text-xs text-[var(--text-secondary)]">
                        <GitBranch className="h-3 w-3" />
                        {picked.byUrl && picked.byUrl.branches.length > 1 ? (
                          <Select
                            aria-label="Branch"
                            value={picked.repo.defaultBranch}
                            onChange={(e) =>
                              setPicked({ ...picked, repo: { ...picked.repo, defaultBranch: e.target.value } })
                            }
                            size="sm"
                      wrapperClassName="w-auto max-w-40"
                      className="font-mono text-xs"
                          >
                            {picked.byUrl.branches.map((b) => (
                              <option key={b} value={b}>
                                {b}
                              </option>
                            ))}
                          </Select>
                        ) : (
                          <span className="font-mono">{picked.repo.defaultBranch}</span>
                        )}
                      </span>
                      <button
                        type="button"
                        onClick={unpick}
                        className="text-xs text-[var(--text-secondary)] underline hover:text-[var(--text-primary)]"
                      >
                        Change
                      </button>
                    </span>
                  </div>
                  <AccessCheck
                    picked={picked}
                    appConfigured={repoSources.appConfigured}
                    detectionFailed={detection.state === "failed"}
                  />
                  <RepoReadFailureCallout
                    detection={detection}
                    installURL={repoSources.installURL}
                    isAdmin={repoSources.isAdmin}
                    onContinue={kind === "web" ? chooseRuntimeManually : undefined}
                  />
                </div>
              ) : (
                <RepoPicker onPick={pick} searchRef={repoSearchRef} error={fieldErrors.repo} autoFocus />
              )}
            </div>
          ) : (
            <div className="mt-4 space-y-4">
              <FormField
                label="Image"
                hint="Full registry path. Paste a full reference and the tag moves to its own field."
                error={fieldErrors.image}
                htmlFor="svc-image"
              >
                <Input
                  id="svc-image"
                  ref={imageInputRef}
                  value={imageRepo}
                  onChange={(e) => {
                    setImageRepo(e.target.value);
                    clearFieldError("image");
                  }}
                  onPaste={(e) => {
                    const text = e.clipboardData.getData("text");
                    const el = e.currentTarget;
                    // Only when the paste replaces the whole field.
                    if (el.selectionStart === 0 && el.selectionEnd === el.value.length && splitImageInput(text)) e.preventDefault();
                  }}
                  onBlur={(e) => {
                    if (splitImageInput(e.target.value)) return;
                    if (e.target.value.trim()) setFieldErrors((prev) => ({ ...prev, image: imageRepositoryProblem(e.target.value) ?? undefined }));
                  }}
                  placeholder="e.g. ghcr.io/acme/api"
                  aria-invalid={fieldErrors.image ? true : undefined}
                  spellCheck={false}
                  className={monoInputClass}
                />
              </FormField>
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <FormField
                  label="Tag"
                  hint="Required. Immutable tags or digests roll out predictably; latest doesn't."
                  error={fieldErrors.tag}
                  htmlFor="svc-tag"
                >
                  <Input
                    id="svc-tag"
                    ref={tagInputRef}
                    value={imageTag}
                    onChange={(e) => {
                      setImageTag(e.target.value);
                      clearFieldError("tag");
                    }}
                    onBlur={(e) => {
                      if (e.target.value.trim()) setFieldErrors((prev) => ({ ...prev, tag: imageTagProblem(e.target.value) ?? undefined }));
                    }}
                    aria-invalid={fieldErrors.tag ? true : undefined}
                    placeholder="e.g. v1.4.2"
                    spellCheck={false}
                    className={monoInputClass}
                  />
                </FormField>
                <FormField
                  label="Registry login"
                  hint={
                    (registryCreds.data ?? []).length === 0
                      ? "None saved, so public images only."
                      : pullSecretChoice === null && autoPullSecret
                        ? `Matched ${imageRegistryHost(imageRepo)}.`
                        : "For private registries."
                  }
                  htmlFor="svc-pull-secret"
                >
                  <Select
                    id="svc-pull-secret"
                    value={pullSecret}
                    onChange={(e) => setPullSecretChoice(e.target.value)}
                    disabled={(registryCreds.data ?? []).length === 0}
                  >
                    <option value="">None (public image)</option>
                    {(registryCreds.data ?? []).map((c) => (
                      <option key={c.secretName} value={c.secretName}>
                        {c.registry} · {c.username}
                      </option>
                    ))}
                  </Select>
                </FormField>
              </div>
            </div>
          )}
        </Step>

        {fastPath ? (
          <Step
            n={2}
            title={quickCopy.title}
            description={quickCopy.description}
            last
          >
            <DetectedSettings
              project={project}
              name={name}
              slug={slug}
              runtime={runtime}
              port={port}
              path={path}
              branch={picked.repo.defaultBranch}
              detection={detection}
              copy={quickCopy}
              addonSuggestions={addonSuggestions}
              addonPicks={addonPicks}
              onToggleAddon={toggleAddonPick}
            />
            <div className="mt-4 flex flex-col-reverse gap-3 border-t border-[var(--border-subtle)] pt-4 sm:flex-row sm:items-center sm:justify-between">
              <Button type="button" variant="ghost" size="sm" onClick={() => setAdvanced(true)} className="self-start sm:self-auto">
                <SlidersHorizontal className="h-3.5 w-3.5" />
                Edit settings
              </Button>
              <div className="flex flex-col-reverse gap-2 sm:flex-row sm:items-center">
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => void onAdd(false)}
                  disabled={submitDisabled}
                  title="Create the service without building it. Add variables or addons, then deploy from the Deployments tab."
                  className="h-9 px-4"
                >
                  Create without deploying
                </Button>
                <Button type="button" onClick={() => void onAdd()} disabled={submitDisabled} className="h-9 px-4">
                  {detection.state === "detecting" ? (
                    <Loader2 className="h-4 w-4 motion-safe:animate-spin" />
                  ) : (
                    <Rocket className="h-4 w-4" />
                  )}
                  {submitting ? "Creating…" : detection.state === "detecting" ? "Detecting…" : "Deploy now"}
                </Button>
              </div>
            </div>
          </Step>
        ) : (
          <>
        <Step
            n={2}
            title="Configure"
            description={
              !showConfigure
                ? "Pick a source first."
                : source === "repo" && kind === "web" && quickCopy.runtime === "default"
                  ? "Name, build and size. Detection didn't find a runtime, so check the build settings."
                  : source === "repo" && kind === "web"
                    ? "Name, build and size. Detected values are prefilled."
                    : "Name, build and size."
            }
            disabled={!showConfigure}
          >
            {showConfigure && (
              <div className="space-y-5">
                {advanced && source === "repo" && kind === "web" && (
                  <button
                    type="button"
                    onClick={() => setAdvanced(false)}
                    className="text-xs text-[var(--link)] underline-offset-4 hover:underline"
                  >
                    Back to the detected-settings summary
                  </button>
                )}
                <div className="grid grid-cols-1 gap-4 sm:grid-cols-[1fr_9rem]">
                  <FormField
                    label="Service name"
                    hint={slug ? <>URL slug: <span className="font-mono">{slug}</span></> : "Letters, digits, spaces and dashes."}
                    error={fieldErrors.name}
                    htmlFor="svc-name"
                  >
                    <Input
                      id="svc-name"
                      ref={nameInputRef}
                      value={name}
                      onChange={(e) => {
                        setName(e.target.value);
                        clearFieldError("name");
                      }}
                      placeholder="e.g. Todo API"
                      aria-invalid={fieldErrors.name ? true : undefined}
                      className={inputClass}
                    />
                  </FormField>
                  {(source === "image" || kind === "web") && (
                    <FormField
                      label="Port"
                      hint={source === "image" ? "Defaults to 8080." : "Detected, or 8080."}
                      htmlFor="svc-port"
                    >
                      <Input
                        id="svc-port"
                        type="number"
                        inputMode="numeric"
                        value={port}
                        onChange={(e) => setPort(e.target.value)}
                        placeholder="e.g. 3000"
                        className={monoInputClass}
                      />
                    </FormField>
                  )}
                </div>
  
                {source === "repo" && (
                  <>
                    <FormField
                      label="Root directory"
                      hint="For monorepos: the folder this service lives in. Leave empty for the repo root."
                      optional
                      htmlFor="svc-path"
                    >
                      <Input
                        id="svc-path"
                        value={path}
                        onChange={(e) => setPath(e.target.value)}
                        placeholder="e.g. apps/api"
                        spellCheck={false}
                        className={monoInputClass}
                      />
                    </FormField>
                    <FormField
                      label="Service type"
                      hint={
                        kind === "web"
                          ? "Serves HTTP on a public URL."
                          : "Runs a sibling service's image with its own command. No port, URL or health checks."
                      }
                    >
                      <SegmentedControl
                        label="Service type"
                        value={kind}
                        options={[
                          { value: "web", label: "Web service" },
                          { value: "worker", label: "Worker" },
                        ]}
                        onChange={(k) => {
                          setKind(k);
                          clearFieldError("fromService");
                          clearFieldError("runtime");
                        }}
                      />
                    </FormField>
                    {kind === "web" && (
                      <FormField label="Runtime" hint="How kuso builds the image." error={fieldErrors.runtime}>
                        <div ref={runtimeRef} className="space-y-2">
                          <div className="flex items-center gap-2">
                            <RuntimeIcon runtime={runtime || "dockerfile"} />
                            <SegmentedControl
                              label="Runtime"
                              value={runtime}
                              options={RUNTIMES}
                              onChange={(r) => {
                                setRuntime(r);
                                clearFieldError("runtime");
                              }}
                            />
                          </div>
                          <RuntimeExplainer detection={detection} path={path} runtime={runtime} />
                        </div>
                      </FormField>
                    )}
                    {kind === "web" && runtime === "dockerfile" && (
                      <FormField
                        label="Dockerfile path"
                        hint="Relative to the root directory. Only for a non-standard name."
                        optional
                        htmlFor="svc-dockerfile"
                      >
                        <Input
                          id="svc-dockerfile"
                          value={dockerfile}
                          onChange={(e) => setDockerfile(e.target.value)}
                          placeholder="e.g. docker/Dockerfile.prod"
                          spellCheck={false}
                          className={monoInputClass}
                        />
                      </FormField>
                    )}
                    {kind === "worker" && (
                      <>
                        <FormField
                          label="Runs the image of"
                          hint="The sibling service whose built image this worker reuses."
                          error={fieldErrors.fromService}
                        >
                          {workerSources.length === 0 ? (
                            <p className="text-xs text-[var(--text-secondary)]">
                              No buildable services in this project yet. Add a web service first, then come back for
                              its worker.
                            </p>
                          ) : (
                            <SegmentedControl
                              label="Image source"
                              value={fromService}
                              options={workerSources.map((s) => ({ value: s, label: s }))}
                              onChange={(s) => {
                                setFromService(s);
                                clearFieldError("fromService");
                              }}
                            />
                          )}
                        </FormField>
                        <FormField label="Command" hint="The process to run instead of the image's default." htmlFor="svc-command">
                          <Input
                            id="svc-command"
                            value={command}
                            onChange={(e) => setCommand(e.target.value)}
                            placeholder="e.g. bundle exec sidekiq"
                            spellCheck={false}
                            className={monoInputClass}
                          />
                        </FormField>
                      </>
                    )}
                  </>
                )}
  
                <SizingFields value={sizing} onChange={setSizing} presets={podSizes.data ?? []} error={fieldErrors.sizing} />
  
                {source === "repo" && addonSuggestions.length > 0 && (
                  <FormField
                    label="Suggested addons"
                    hint="Found in the repo's .env.example or docker-compose. Selected ones are created with the service."
                  >
                    <div className="flex flex-wrap gap-1.5">
                      {addonSuggestions.map((sug) => {
                        const on = addonPicks.has(sug.kind);
                        return (
                          <button
                            key={sug.kind}
                            type="button"
                            aria-pressed={on}
                            title={sug.reason}
                            onClick={() => toggleAddonPick(sug.kind)}
                            className={
                              "inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs " +
                              (on
                                ? "border-[var(--accent)] bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                                : "border-[var(--border-subtle)] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]")
                            }
                          >
                            {on ? <Check className="h-3 w-3" /> : <Plus className="h-3 w-3" />}
                            {addonLabel(sug.kind)}
                          </button>
                        );
                      })}
                    </div>
                  </FormField>
                )}
              </div>
            )}
          </Step>
  
          <Step
            n={3}
            title="Variables"
            optional
            description="Paste or upload a .env so the first deploy starts with its config."
            disabled={!showConfigure}
          >
            {showConfigure && (
              <div ref={envRef} className="space-y-2">
                {fieldErrors.env && (
                  <p role="alert" className="text-xs text-[var(--error)]">
                    {fieldErrors.env}
                  </p>
                )}
                <EnvPastePanel
                  text={envText}
                  onChange={(t) => {
                    setEnvText(t);
                    clearFieldError("env");
                  }}
                  parsed={env}
                />
              </div>
            )}
          </Step>
  
          <Step n={4} title="Deploy" last disabled={!showConfigure}>
            {showConfigure && (
              <div className="space-y-4">
                <DeploySummary
                  project={project}
                  slug={slug}
                  runtime={source === "image" ? "image" : effectiveRuntime}
                  branch={source === "repo" && kind === "web" ? picked?.repo.defaultBranch : undefined}
                  image={source === "image" ? imageRefString(imageRepo.trim() || "…", imageTag.trim() || "…") : undefined}
                  envCount={env.vars.length}
                  addons={source === "repo" ? [...addonPicks].filter((k) => addonSuggestions.some((s) => s.kind === k)) : []}
                  worker={source === "repo" && kind === "worker" ? fromService : undefined}
                />
                <div className="flex flex-col-reverse gap-3 sm:flex-row sm:items-center sm:justify-between">
                  <Link
                    href={`/projects/${encodeURIComponent(project)}`}
                    className="text-center text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
                  >
                    Cancel
                  </Link>
                  <div className="flex flex-col-reverse gap-2 sm:flex-row sm:items-center">
                    {willBuild && (
                      <Button
                        type="button"
                        variant="secondary"
                        onClick={() => void onAdd(false)}
                        disabled={submitDisabled}
                        title="Create the service without building it. Add variables or addons, then deploy from the Deployments tab."
                        className="h-9 px-4"
                      >
                        Create without deploying
                      </Button>
                    )}
                    <Button type="button" onClick={() => void onAdd()} disabled={submitDisabled} className="h-9 px-4">
                      <Rocket className="h-4 w-4" />
                      {submitLabel}
                    </Button>
                  </div>
                </div>
              </div>
            )}
          </Step>
          </>
        )}
      </ol>
    </div>
  );
}

function Step({
  n,
  title,
  description,
  optional,
  done,
  disabled,
  last,
  children,
}: {
  n: number;
  title: string;
  description?: string;
  optional?: boolean;
  done?: boolean;
  disabled?: boolean;
  last?: boolean;
  children?: React.ReactNode;
}) {
  return (
    <li className="relative flex gap-3 sm:gap-4" aria-disabled={disabled || undefined}>
      <div className="flex flex-col items-center">
        <span
          className={
            "flex h-7 w-7 shrink-0 items-center justify-center rounded-full border text-xs font-semibold " +
            (disabled
              ? "border-[var(--border-subtle)] text-[var(--text-tertiary)]"
              : done
                ? "border-[var(--accent)] bg-[var(--accent)] text-[var(--accent-foreground)]"
                : "border-[var(--accent)] text-[var(--accent)]")
          }
        >
          {done && !disabled ? <Check className="h-3.5 w-3.5" /> : n}
        </span>
        {!last && <span className="mt-1 w-px flex-1 bg-[var(--border-subtle)]" aria-hidden />}
      </div>
      <section className={"min-w-0 flex-1 " + (last ? "pb-2" : "pb-8")}>
        <h2 className={"text-[15px] font-semibold leading-7 " + (disabled ? "text-[var(--text-tertiary)]" : "")}>
          {title}
          {optional && <span className="ml-2 text-xs font-normal text-[var(--text-tertiary)]">optional</span>}
        </h2>
        {description && <p className="mb-3 text-[13px] text-[var(--text-secondary)]">{description}</p>}
        {!description && <div className="mb-3" />}
        {children && (
          <div className="rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">{children}</div>
        )}
      </section>
    </li>
  );
}

function SourceCard({
  active,
  onClick,
  icon,
  title,
  hint,
}: {
  active: boolean;
  onClick: () => void;
  icon: React.ReactNode;
  title: string;
  hint: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={
        "flex flex-col gap-1 rounded-md border px-3 py-2.5 text-left transition-colors " +
        (active
          ? "border-[var(--accent)] bg-[var(--accent-subtle)]"
          : "border-[var(--border-subtle)] bg-[var(--bg-primary)] hover:bg-[var(--bg-tertiary)]")
      }
    >
      <span className="flex items-center gap-2 text-sm font-medium">
        {icon}
        {title}
      </span>
      <span className="text-xs text-[var(--text-secondary)]">{hint}</span>
    </button>
  );
}

// DetectedSettings is the quick path's summary: each detected value with
// where it came from, so "Deploy now" is an informed click.
function DetectedSettings({
  project,
  name,
  slug,
  runtime,
  port,
  path,
  branch,
  detection,
  copy,
  addonSuggestions,
  addonPicks,
  onToggleAddon,
}: {
  project: string;
  name: string;
  slug: string;
  runtime: string;
  port: string;
  path: string;
  branch: string;
  detection: ReturnType<typeof useRepoDetection>;
  copy: DetectionCopy;
  addonSuggestions: AddonSuggestion[];
  addonPicks: Set<string>;
  onToggleAddon: (kind: string) => void;
}) {
  const detecting = detection.state === "detecting";
  const result = detection.result;
  const found = detection.state === "done" && result && result.reason !== "fallback";
  const where = repoWhere(path);
  const runtimeWhy = detecting
    ? "Detecting…"
    : found
      ? `${result.reason.replace(/ detected$/, "")} found in ${where}`
      : detection.state === "failed"
        ? "Not detected; nixpacks inspects the code at build time"
        : `No Dockerfile or app manifest in ${where}; nixpacks inspects the code at build time`;
  const portWhy = detecting
    ? "Detecting…"
    : result?.port
      ? `From ${result.reason.replace(/ detected$/, "")}`
      : detection.state === "failed"
        ? "Not detected, so kuso uses 8080 and sets $PORT to match"
        : "None declared in the repo; kuso uses 8080 and sets $PORT to match";
  const rows: { label: string; value: React.ReactNode; why?: string; marker?: Provenance | null }[] = [
    {
      label: "Service",
      value: (
        <>
          {name.trim() || "…"}
          {slug && slug !== name.trim() && <span className="text-[var(--text-tertiary)]"> · {slug}</span>}
        </>
      ),
      why: `In ${project}`,
    },
    {
      label: "Runtime",
      value: (
        <span className="inline-flex items-center gap-1.5">
          {runtime && <RuntimeIcon runtime={runtime} />}
          {runtime || "…"}
        </span>
      ),
      why: runtimeWhy,
      marker: copy.runtime,
    },
    { label: "Port", value: port || "8080", why: portWhy, marker: copy.port },
    { label: "Root directory", value: path.trim() || "/", why: path.trim() ? undefined : "Repo root. Set one for a monorepo." },
    { label: "Branch", value: branch, why: "Every push builds and deploys" },
  ];
  return (
    <div className="space-y-4">
      <dl className="grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2">
        {rows.map((r) => (
          <div key={r.label} className="min-w-0">
            <dt className="flex min-h-5 items-center gap-1.5 text-xs text-[var(--text-secondary)]">
              {r.label}
              {r.marker && <ProvenanceMarker kind={r.marker} />}
            </dt>
            <dd className="mt-0.5 truncate font-mono text-[13px] text-[var(--text-primary)]">{r.value}</dd>
            {r.why && (
              <dd className={"mt-0.5 text-xs " + (detecting ? "text-[var(--text-tertiary)]" : "text-[var(--text-secondary)]")}>
                {r.why}
              </dd>
            )}
          </div>
        ))}
      </dl>
      {addonSuggestions.length > 0 && (
        <div className="space-y-1.5">
          <p className="text-xs text-[var(--text-secondary)]">
            The repo looks like it uses these. Selected ones are created with the service.
          </p>
          <div className="flex flex-wrap gap-1.5">
            {addonSuggestions.map((sug) => {
              const on = addonPicks.has(sug.kind);
              return (
                <button
                  key={sug.kind}
                  type="button"
                  aria-pressed={on}
                  title={sug.reason}
                  onClick={() => onToggleAddon(sug.kind)}
                  className={
                    "inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs " +
                    (on
                      ? "border-[var(--accent)] bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                      : "border-[var(--border-subtle)] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]")
                  }
                >
                  {on ? <Check className="h-3 w-3" /> : <Plus className="h-3 w-3" />}
                  {addonLabel(sug.kind)}
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

function repoWhere(path: string): string {
  const p = path.trim().replace(/^\/+|\/+$/g, "");
  return p ? `${p}/` : "the repo root";
}

function ProvenanceMarker({ kind }: { kind: Provenance }) {
  return (
    <Badge
      variant={kind === "detected" ? "success" : "neutral"}
      title={kind === "detected" ? "Read from the repository" : "kuso's default; detection didn't supply it"}
    >
      {kind}
    </Badge>
  );
}

function DeploySummary({
  project,
  slug,
  runtime,
  branch,
  image,
  envCount,
  addons,
  worker,
}: {
  project: string;
  slug: string;
  runtime: string;
  branch?: string;
  image?: string;
  envCount: number;
  addons: string[];
  worker?: string;
}) {
  const items: React.ReactNode[] = [
    <>
      Creates <span className="font-mono text-[var(--text-primary)]">{slug || "…"}</span> in{" "}
      <span className="font-mono text-[var(--text-primary)]">{project}</span>
      {runtime ? (
        <>
          {" "}
          (<span className="font-mono">{runtime}</span>)
        </>
      ) : null}
    </>,
  ];
  if (envCount > 0) items.push(<>Saves {envCount} {envCount === 1 ? "variable" : "variables"}</>);
  for (const a of addons) items.push(<>Adds a {addonLabel(a)} addon</>);
  if (branch) items.push(<>Starts the first build from <span className="font-mono">{branch}</span> and deploys it when it passes</>);
  if (image) items.push(<>Deploys <span className="font-mono break-all">{image}</span></>);
  if (worker) items.push(<>Runs on <span className="font-mono">{worker}</span>&apos;s image, deploying with its builds</>);
  return (
    <ul className="space-y-1 text-[13px] text-[var(--text-secondary)]">
      {items.map((it, i) => (
        <li key={i} className="flex gap-2">
          <Check className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
          <span className="min-w-0">{it}</span>
        </li>
      ))}
    </ul>
  );
}

function SizingFields({
  value,
  onChange,
  presets,
  error,
}: {
  value: SizingInput;
  onChange: (next: SizingInput) => void;
  presets: { Name: string; CPURequest: string; MemoryRequest: string; MemoryLimit: string }[];
  error?: string;
}) {
  return (
    <div className="space-y-1.5">
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-[1fr_7rem_7rem]">
        <div className="col-span-2 sm:col-span-1">
          <FormField label="Size" hint="CPU and memory per pod." htmlFor="svc-size">
            <Select
              id="svc-size"
              value={value.size}
              onChange={(e) => onChange({ ...value, size: e.target.value })}
            >
              <option value="">Instance default</option>
              {presets.map((p) => (
                <option key={p.Name} value={p.Name}>
                  {p.Name} · {p.CPURequest || "-"} / {p.MemoryRequest || "-"}
                  {p.MemoryLimit ? ` (max ${p.MemoryLimit})` : ""}
                </option>
              ))}
              <option value={SIZE_NONE}>None (no requests or limits)</option>
            </Select>
          </FormField>
        </div>
        <FormField label="Replicas" hint="Minimum." htmlFor="svc-replicas">
          <Input
            id="svc-replicas"
            type="number"
            inputMode="numeric"
            min={1}
            value={value.replicas}
            onChange={(e) => onChange({ ...value, replicas: e.target.value })}
            placeholder="1"
            aria-invalid={error ? true : undefined}
            className={monoInputClass}
          />
        </FormField>
        <FormField label="Max replicas" hint="Autoscale cap." htmlFor="svc-max-replicas">
          <Input
            id="svc-max-replicas"
            type="number"
            inputMode="numeric"
            min={1}
            value={value.maxReplicas}
            onChange={(e) => onChange({ ...value, maxReplicas: e.target.value })}
            placeholder="5"
            aria-invalid={error ? true : undefined}
            className={monoInputClass}
          />
        </FormField>
      </div>
      {error && (
        <p role="alert" className="text-xs text-[var(--error)]">
          {error}
        </p>
      )}
    </div>
  );
}
