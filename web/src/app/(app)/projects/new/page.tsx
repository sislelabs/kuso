"use client";

import { PageHeader } from "@/components/shared/PageHeader";
import { useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  Archive,
  ArrowRight,
  Box,
  Check,
  ChevronRight,
  FileUp,
  FolderPlus,
  Github,
  GitBranch,
  Globe,
  Rocket,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { Input } from "@/components/ui/input";
import { projectQueryKey, useCreateProject, useProjects } from "@/features/projects";
import {
  friendlyApiError,
  projectNameError,
  projectSlugFromRepo,
  serviceSlugError,
  SERVICE_DISPLAY_NAME_RE,
} from "@/features/projects/names";
import { buildCreateProjectBody, DEFAULT_PREVIEW_TTL_DAYS } from "@/features/projects/createProjectBody";
import { buildCreateServiceBody } from "@/features/services/createServiceBody";
import { createServiceWithEnv } from "@/features/services/createServiceFlow";
import { slugifyServiceName } from "@/features/services/slug";
import { useInstallations } from "@/features/github";
import { HostClashHint } from "@/components/shared/HostClashHint";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { useCan, Perms } from "@/features/auth";
import { defaultServiceHost, useInstanceDomain } from "@/lib/default-host";
import { defaultServiceName } from "@/lib/default-service-name";
import { getDefaultDomain } from "@/features/cluster-config/api";
import { restoreFormDraft } from "@/lib/query-client";
import { ImportArchiveCard } from "./ImportArchiveCard";
import { DeployImageForm, type DeployImageInput } from "./DeployImageForm";
import { loginRegistry } from "@/features/registry-credentials/api";
import { specImageTag } from "@/features/services/imageRef";
import { TemplateCard } from "./TemplateCard";
import { FormField, SegmentedControl } from "../[project]/services/new/FormField";
import { RepoPicker, pickedRepoURL, useRepoSources, type PickedRepo } from "../[project]/services/new/RepoPicker";
import { useRepoDetection } from "../[project]/services/new/useRepoDetection";
import { AccessCheck, RepoReadFailureCallout, RuntimeExplainer } from "../[project]/services/new/RepoInsights";
import { EnvPastePanel, useDotenv } from "../[project]/services/new/EnvPastePanel";
import { reportCreateOutcome } from "../[project]/services/new/reportCreateOutcome";
import { servicePageHref } from "@/components/service/workspace/hrefs";

type Secondary = "archive" | "empty" | null;

const RUNTIMES = [
  { value: "dockerfile", label: "Dockerfile" },
  { value: "nixpacks", label: "Nixpacks" },
  { value: "static", label: "Static" },
  { value: "buildpacks", label: "Buildpacks" },
] as const;

const inputClass = "h-9 text-[13px]";
const monoInputClass = "h-9 font-mono text-[13px]";

// NewProjectPage leads with importing a Git repo (project + first
// service + first build in one step). Templates, compose and archive
// imports and an empty project sit below as secondary options.
export default function NewProjectPage() {
  const router = useRouter();
  const qc = useQueryClient();
  const create = useCreateProject();
  const projects = useProjects();
  // The compose and archive importers are admin-only server-side.
  const isAdmin = useCan(Perms.SettingsAdmin);
  const repoSources = useRepoSources();
  const installations = useInstallations({ enabled: isAdmin });
  const instanceDomain = useInstanceDomain() || "kuso.example.com";
  // The server's own default domain, for promises that must be exact.
  // Undefined while loading or on a server too old to say.
  const serverDomain = useQuery({ queryKey: ["config", "domain"], queryFn: getDefaultDomain, staleTime: Infinity, retry: false }).data;

  const taken = useMemo(() => new Set((projects.data ?? []).map((p) => p.metadata.name)), [projects.data]);

  // Shared by both create paths (only one is ever submitted).
  const [baseDomain, setBaseDomain] = useState("");
  const [previewsEnabled, setPreviewsEnabled] = useState(true);
  const [previewsTTLDays, setPreviewsTTLDays] = useState("");
  const [namespace, setNamespace] = useState("");
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  // The primary card builds from a repo or runs a pre-built image.
  const [primary, setPrimary] = useState<"repo" | "image">("repo");

  // ---- Repo-first path ----
  const [picked, setPicked] = useState<PickedRepo | null>(null);
  const [projectName, setProjectName] = useState("");
  const [projectNameTouched, setProjectNameTouched] = useState(false);
  const [serviceName, setServiceName] = useState("");
  const [serviceNameTouched, setServiceNameTouched] = useState(false);
  const [path, setPath] = useState("");
  const [runtime, setRuntime] = useState("");
  const [port, setPort] = useState("");
  const [buildOpen, setBuildOpen] = useState(false);
  const [envOpen, setEnvOpen] = useState(false);
  const [envText, setEnvText] = useState("");
  const env = useDotenv(envText);
  const [importErrors, setImportErrors] = useState<{ project?: string; service?: string; env?: string }>({});
  const importNameRef = useRef<HTMLInputElement>(null);
  const runtimeRef = useRef<HTMLDivElement>(null);
  // Detection failed: open Build settings and land on the runtime picker.
  const chooseRuntimeManually = () => {
    setBuildOpen(true);
    requestAnimationFrame(() => {
      runtimeRef.current?.scrollIntoView({ block: "center", behavior: "smooth" });
      runtimeRef.current?.querySelector<HTMLButtonElement>("button[aria-pressed=true], button")?.focus({ preventScroll: true });
    });
  };
  const detection = useRepoDetection(picked, path, (rt, p) => {
    setRuntime(rt);
    if (p) setPort(String(p));
  });

  const repoName = picked ? (picked.repo.fullName.split("/").pop() ?? "") : "";

  // Re-derive the project name when the taken list arrives, until edited.
  useEffect(() => {
    if (!picked || projectNameTouched) return;
    setProjectName(projectSlugFromRepo(repoName, taken));
  }, [picked, repoName, taken, projectNameTouched]);
  useEffect(() => {
    if (!picked || serviceNameTouched) return;
    setServiceName(defaultServiceName(projectName, repoName));
  }, [picked, projectName, repoName, serviceNameTouched]);

  const onPick = (p: PickedRepo) => {
    setPicked(p);
    setProjectNameTouched(false);
    setServiceNameTouched(false);
    setImportErrors({});
    setFormError(null);
    setSecondary(null);
  };
  const unpick = () => {
    setPicked(null);
    setPath("");
    setRuntime("");
    setPort("");
    setImportErrors({});
  };

  const projectNameProblem = (n: string): string | null =>
    projectNameError(n) ?? (taken.has(n) ? `A project named "${n}" already exists.` : null);
  const serviceSlug = slugifyServiceName(serviceName);
  const serviceProblem = (): string | null => {
    const n = serviceName.trim();
    if (!n) return "Service name is required.";
    if (!serviceSlug) return "Name needs at least one letter or digit.";
    if (!SERVICE_DISPLAY_NAME_RE.test(n)) return "Use letters, digits, spaces and dashes only (max 60).";
    return serviceSlugError(projectName.trim() || "x", serviceSlug);
  };
  const liveProjectError = projectNameTouched && projectName ? projectNameProblem(projectName.trim()) : null;

  const onImport = async (deploy = true) => {
    if (!picked) return;
    const pName = projectName.trim();
    const errs = {
      project: projectNameProblem(pName) ?? undefined,
      service: serviceProblem() ?? undefined,
      env: env.errors.length > 0 ? `${env.errors.length === 1 ? "A line" : `${env.errors.length} lines`} in the .env can't be read; fix or remove ${env.errors.length === 1 ? "it" : "them"}.` : undefined,
    };
    setImportErrors(errs);
    if (errs.project) {
      importNameRef.current?.focus();
      return;
    }
    if (errs.service) {
      setBuildOpen(true);
      return;
    }
    if (errs.env) {
      setEnvOpen(true);
      return;
    }
    const repoURL = pickedRepoURL(picked);
    const branch = picked.repo.defaultBranch;
    const installationId = picked.byUrl ? 0 : picked.installationId;
    const pBody = buildCreateProjectBody({
      name: pName,
      description: `Imported from ${picked.repo.fullName}`,
      baseDomain,
      namespace,
      repoURL,
      repoBranch: branch,
      installationId,
      previewsEnabled,
      previewsTTLDays,
    });
    if ("error" in pBody) {
      setFormError(pBody.error);
      setAdvancedOpen(true);
      return;
    }
    setFormError(null);
    setSubmitting(true);
    try {
      await create.mutateAsync(pBody);
    } catch (err) {
      toast.error(friendlyApiError(err, "Failed to create project"));
      setSubmitting(false);
      return;
    }
    const sBody = buildCreateServiceBody({
      source: "repo",
      name: serviceName,
      repo: { url: repoURL, branch, path, token: picked.byUrl?.token, installationId },
      projectRepo: { url: repoURL, defaultBranch: branch },
      kind: "web",
      runtime: runtime || "nixpacks",
      port,
    });
    const projectHref = `/projects/${encodeURIComponent(pName)}`;
    try {
      const out = await createServiceWithEnv(pName, sBody, { envVars: env.vars, deploy });
      await qc.invalidateQueries({ queryKey: projectQueryKey(pName) });
      reportCreateOutcome(out, `${pName}/${sBody.name}`, branch);
      router.replace(servicePageHref(pName, sBody.name, { tab: "deployments" }));
    } catch (err) {
      // The project exists; send the user to finish the service there
      // rather than leaving them on a form that would now 409.
      toast.error(
        `Project ${pName} was created, but its service wasn't: ${friendlyApiError(err, "unknown error")}. Add the service again below.`,
        { duration: Infinity, closeButton: true },
      );
      router.replace(`${projectHref}/services/new`);
    } finally {
      setSubmitting(false);
    }
  };

  // ---- Image path: project + one image service, no build ----
  const onDeployImage = async ({ projectName: pName, image, port: imagePort, envVars, registryLogin }: DeployImageInput) => {
    const pBody = buildCreateProjectBody({
      name: pName,
      description: `Runs ${image.repository}`,
      baseDomain,
      namespace,
      repoURL: "",
      repoBranch: "",
      installationId: 0,
      previewsEnabled,
      previewsTTLDays,
    });
    if ("error" in pBody) {
      setFormError(pBody.error);
      setAdvancedOpen(true);
      return;
    }
    setFormError(null);
    setSubmitting(true);
    try {
      await create.mutateAsync(pBody);
    } catch (err) {
      toast.error(friendlyApiError(err, "Failed to create project"));
      setSubmitting(false);
      return;
    }
    const projectHref = `/projects/${encodeURIComponent(pName)}`;
    // A private image's login goes on the project before the service
    // exists, so the first pull already has it.
    let pullSecret = "";
    if (registryLogin) {
      try {
        const cred = await loginRegistry(pName, {
          registry: registryLogin.registry.trim(),
          username: registryLogin.username.trim(),
          password: registryLogin.password,
        });
        pullSecret = cred.secretName;
      } catch (err) {
        toast.error(
          `Project ${pName} was created, but the registry login wasn't saved: ${friendlyApiError(err, "unknown error")}. Add the service below and log in there.`,
          { duration: Infinity, closeButton: true },
        );
        router.replace(`${projectHref}/services/new`);
        setSubmitting(false);
        return;
      }
    }
    const sBody = buildCreateServiceBody({
      source: "image",
      name: defaultServiceName(pName, image.name),
      image: image.repository,
      tag: specImageTag(image.tag),
      port: imagePort,
      pullSecret,
    });
    try {
      const out = await createServiceWithEnv(pName, sBody, { envVars });
      await qc.invalidateQueries({ queryKey: projectQueryKey(pName) });
      reportCreateOutcome(out, `${pName}/${sBody.name}`);
      router.replace(servicePageHref(pName, sBody.name, { tab: "deployments" }));
    } catch (err) {
      toast.error(
        `Project ${pName} was created, but its service wasn't: ${friendlyApiError(err, "unknown error")}. Add the service again below.`,
        { duration: Infinity, closeButton: true },
      );
      router.replace(`${projectHref}/services/new`);
    } finally {
      setSubmitting(false);
    }
  };

  // ---- Secondary paths ----
  const [secondary, setSecondary] = useState<Secondary>(null);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [repoURL, setRepoURL] = useState("");
  const [repoBranch, setRepoBranch] = useState("");
  const [installationId, setInstallationId] = useState(0);
  const [nameError, setNameError] = useState<string | null>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const emptyRef = useRef<HTMLDivElement>(null);

  // Post-login draft restore: if the session expired mid-create (->
  // /login -> back here), reopen the empty-project form with what was
  // typed. Drafts older than 30 min are dropped by restoreFormDraft.
  useEffect(() => {
    const draft = restoreFormDraft();
    if (!draft) return;
    if (draft.name || draft.description || draft.baseDomain) setSecondary("empty");
    if (draft.name) setName(draft.name);
    if (draft.description) setDescription(draft.description);
    if (draft.baseDomain) {
      setBaseDomain(draft.baseDomain);
      setAdvancedOpen(true);
    }
  }, []);

  useEffect(() => {
    if (secondary) emptyRef.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [secondary]);

  const onCreateEmpty = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    const invalid = projectNameProblem(trimmed);
    if (invalid) {
      setNameError(invalid);
      nameInputRef.current?.focus();
      return;
    }
    setNameError(null);
    const body = buildCreateProjectBody({
      name: trimmed,
      description,
      baseDomain,
      namespace,
      repoURL,
      repoBranch,
      installationId,
      previewsEnabled,
      previewsTTLDays,
    });
    if ("error" in body) {
      setFormError(body.error);
      setAdvancedOpen(true);
      return;
    }
    setFormError(null);
    setSubmitting(true);
    try {
      await create.mutateAsync(body);
      toast.success("Project created");
      router.replace(`/projects/${encodeURIComponent(trimmed)}/services/new`);
    } catch (err) {
      toast.error(friendlyApiError(err, "Failed to create project"));
    } finally {
      setSubmitting(false);
    }
  };

  const advancedProps = {
    open: advancedOpen,
    onToggle: () => setAdvancedOpen((o) => !o),
    projectName: (secondary === "empty" ? name : projectName).trim(),
    serviceName: secondary === "empty" ? "web" : serviceSlug || "web",
    instanceDomain,
    baseDomain,
    setBaseDomain,
    namespace,
    setNamespace,
    previewsEnabled,
    setPreviewsEnabled,
    previewsTTLDays,
    setPreviewsTTLDays,
  };

  return (
    <div className="mx-auto max-w-3xl px-4 py-6 sm:px-6 lg:py-10">
      <PageHeader
        className="mb-6 sm:mb-6"
        breadcrumb={[{ label: "Projects", href: "/projects" }, { label: "New project" }]}
        title="New project"
        description="Import a Git repository to build and deploy, or run a pre-built container image. A project can hold more services and databases later."
      />

      {/* Primary path: import a repo. */}
      <section
        aria-labelledby="import-heading"
        className="rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
      >
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--border-subtle)] px-4 py-3 sm:px-5">
          <h2 id="import-heading" className="sr-only">
            {primary === "repo" ? "Import a Git repository" : "Deploy a container image"}
          </h2>
          <div role="radiogroup" aria-label="Source" className="inline-flex rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
            {(
              [
                { v: "repo", label: "Import a Git repository", short: "Git repository", icon: <Github className="h-3.5 w-3.5" /> },
                { v: "image", label: "Deploy a container image", short: "Container image", icon: <Box className="h-3.5 w-3.5" /> },
              ] as const
            ).map((o) => (
              <button
                key={o.v}
                type="button"
                role="radio"
                aria-checked={primary === o.v}
                onClick={() => {
                  setPrimary(o.v);
                  setFormError(null);
                }}
                className={
                  "inline-flex min-h-8 items-center gap-1.5 rounded px-2.5 text-[13px] font-medium transition-colors " +
                  (primary === o.v
                    ? "bg-[var(--bg-elevated)] text-[var(--text-primary)] shadow-[var(--shadow-sm)]"
                    : "text-[var(--text-secondary)] hover:text-[var(--text-primary)]")
                }
              >
                {o.icon}
                <span className="sm:hidden">{o.short}</span>
                <span className="hidden sm:inline">{o.label}</span>
              </button>
            ))}
          </div>
          {primary === "repo" && picked && (
            <button
              type="button"
              onClick={unpick}
              className="text-xs text-[var(--text-secondary)] underline hover:text-[var(--text-primary)]"
            >
              Pick another
            </button>
          )}
        </div>
        <div className="p-4 sm:p-5">
          {primary === "image" ? (
            <DeployImageForm
              taken={taken}
              instanceDomain={serverDomain || undefined}
              baseDomain={baseDomain}
              formError={formError}
              submitting={submitting}
              onSubmit={(input) => void onDeployImage(input)}
              advanced={(pName) => <AdvancedFields {...advancedProps} projectName={pName} serviceName={pName || "web"} />}
            />
          ) : !picked ? (
            <RepoPicker onPick={onPick} listClassName="max-h-64" />
          ) : (
            <div className="space-y-5">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-2">
                <span className="flex min-w-0 items-center gap-2 text-[13px]">
                  <Check className="h-3.5 w-3.5 shrink-0 text-[var(--accent)]" />
                  <span className="truncate font-mono">{picked.repo.fullName}</span>
                </span>
                <span className="flex items-center gap-1 text-xs text-[var(--text-secondary)]">
                  <GitBranch className="h-3 w-3" />
                  {picked.byUrl && picked.byUrl.branches.length > 1 ? (
                    <Select
                      aria-label="Branch"
                      value={picked.repo.defaultBranch}
                      onChange={(e) => setPicked({ ...picked, repo: { ...picked.repo, defaultBranch: e.target.value } })}
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
                onContinue={chooseRuntimeManually}
              />

              <FormField
                label="Project name"
                hint={
                  <>
                    Lowercase letters, digits and dashes. Your app will be at{" "}
                    <span className="font-mono text-[var(--text-primary)]">
                      https://
                      {defaultServiceHost(serviceSlug || "web", projectName.trim() || "my-product", baseDomain, instanceDomain)}
                    </span>
                  </>
                }
                error={importErrors.project ?? liveProjectError}
                htmlFor="import-project-name"
              >
                <Input
                  id="import-project-name"
                  ref={importNameRef}
                  value={projectName}
                  onChange={(e) => {
                    setProjectName(e.target.value);
                    setProjectNameTouched(true);
                    setImportErrors((x) => ({ ...x, project: undefined }));
                  }}
                  placeholder="e.g. my-product"
                  aria-invalid={importErrors.project || liveProjectError ? true : undefined}
                  spellCheck={false}
                  autoComplete="off"
                  className={monoInputClass}
                />
              </FormField>

              <div className="space-y-2">
                <p className="flex flex-wrap items-center gap-2 text-[13px]">
                  <RuntimeIcon runtime={runtime || "nixpacks"} />
                  <span className="text-[var(--text-secondary)]">Builds with</span>
                  <span className="font-mono">{runtime || "…"}</span>
                  {port && (
                    <>
                      <span className="text-[var(--text-secondary)]">on port</span>
                      <span className="font-mono">{port}</span>
                    </>
                  )}
                </p>
                <RuntimeExplainer detection={detection} path={path} runtime={runtime} />
              </div>

              <Disclosure
                open={buildOpen}
                onToggle={() => setBuildOpen((o) => !o)}
                title="Build settings"
                summary={importErrors.service ? "fix the service name" : `service ${serviceSlug || "…"} · ${path.trim() || "repo root"}`}
                invalid={!!importErrors.service}
              >
                <div className="space-y-4">
                  <FormField
                    label="Service name"
                    hint={serviceSlug ? <>URL slug: <span className="font-mono">{serviceSlug}</span></> : undefined}
                    error={importErrors.service}
                    htmlFor="import-service-name"
                  >
                    <Input
                      id="import-service-name"
                      value={serviceName}
                      onChange={(e) => {
                        setServiceName(e.target.value);
                        setServiceNameTouched(true);
                        setImportErrors((x) => ({ ...x, service: undefined }));
                      }}
                      placeholder="e.g. web"
                      aria-invalid={importErrors.service ? true : undefined}
                      className={inputClass}
                    />
                  </FormField>
                  <FormField
                    label="Root directory"
                    hint="For monorepos: the folder the app lives in. Leave empty for the repo root."
                    optional
                    htmlFor="import-path"
                  >
                    <Input
                      id="import-path"
                      value={path}
                      onChange={(e) => setPath(e.target.value)}
                      placeholder="e.g. apps/web"
                      spellCheck={false}
                      className={monoInputClass}
                    />
                  </FormField>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-[1fr_8rem]">
                    <FormField label="Runtime" hint="How kuso builds the image.">
                      <div ref={runtimeRef}>
                        <SegmentedControl label="Runtime" value={runtime} options={RUNTIMES} onChange={setRuntime} />
                      </div>
                    </FormField>
                    <FormField label="Port" hint="Detected, or 8080." htmlFor="import-port">
                      <Input
                        id="import-port"
                        type="number"
                        inputMode="numeric"
                        value={port}
                        onChange={(e) => setPort(e.target.value)}
                        placeholder="e.g. 3000"
                        className={monoInputClass}
                      />
                    </FormField>
                  </div>
                </div>
              </Disclosure>

              <Disclosure
                open={envOpen}
                onToggle={() => setEnvOpen((o) => !o)}
                title="Environment variables"
                summary={
                  importErrors.env
                    ? "fix the marked lines"
                    : env.vars.length > 0
                      ? `${env.vars.length} ${env.vars.length === 1 ? "variable" : "variables"}`
                      : "paste or upload a .env"
                }
                invalid={!!importErrors.env}
              >
                {importErrors.env && (
                  <p role="alert" className="mb-2 text-xs text-[var(--error)]">
                    {importErrors.env}
                  </p>
                )}
                <EnvPastePanel
                  id="import-env"
                  text={envText}
                  onChange={(t) => {
                    setEnvText(t);
                    setImportErrors((x) => ({ ...x, env: undefined }));
                  }}
                  parsed={env}
                />
              </Disclosure>

              <AdvancedFields {...advancedProps} />

              {formError && (
                <p role="alert" className="text-xs text-[var(--error)]">
                  {formError}
                </p>
              )}
              <div className="flex flex-col gap-3 border-t border-[var(--border-subtle)] pt-4 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0 space-y-1 text-xs text-[var(--text-secondary)]">
                  <p>
                    Creates the project and its first service, then starts a build from{" "}
                    <span className="font-mono">{picked.repo.defaultBranch}</span>.
                  </p>
                  <p className="break-all">
                    It will be served at{" "}
                    <span className="font-mono text-[var(--text-primary)]">
                      https://
                      {defaultServiceHost(serviceSlug || "web", projectName.trim() || "my-product", baseDomain, instanceDomain)}
                    </span>
                  </p>
                </div>
                <div className="flex flex-col-reverse gap-2 sm:flex-row sm:items-center">
                  <Button
                    type="button"
                    variant="secondary"
                    onClick={() => void onImport(false)}
                    disabled={submitting || detection.state === "detecting"}
                    disabledReason={!submitting && detection.state === "detecting" ? "Still reading the repository to detect how to build it" : undefined}
                    title="Create the project and service without building. Add variables first, then deploy from the Deployments tab."
                    className="h-9 px-4"
                  >
                    Create without deploying
                  </Button>
                  <Button
                    type="button"
                    onClick={() => void onImport()}
                    disabled={submitting || detection.state === "detecting"}
                    disabledReason={!submitting && detection.state === "detecting" ? "Still reading the repository to detect how to build it" : undefined}
                    className="h-9 px-4"
                  >
                    <Rocket className="h-4 w-4" />
                    {submitting ? "Creating…" : "Create & deploy"}
                  </Button>
                </div>
              </div>
            </div>
          )}
        </div>
      </section>

      {/* Secondary paths. */}
      <h2 className="mb-3 mt-8 text-[13px] font-medium text-[var(--text-secondary)]">Or start another way</h2>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <TemplateCard />
        {isAdmin && (
          <OptionCard
            href="/settings/import-compose"
            icon={<FileUp className="h-4 w-4" />}
            title="docker-compose"
            hint="Services and databases from a compose file"
          />
        )}
        {isAdmin && (
          <OptionCard
            icon={<Archive className="h-4 w-4" />}
            title="Project archive"
            hint="Restore a kuso export"
            active={secondary === "archive"}
            onClick={() => setSecondary((s) => (s === "archive" ? null : "archive"))}
          />
        )}
        <OptionCard
          icon={<FolderPlus className="h-4 w-4" />}
          title="Empty project"
          hint="Add services later"
          active={secondary === "empty"}
          onClick={() => {
            setSecondary((s) => (s === "empty" ? null : "empty"));
            setFormError(null);
          }}
        />
      </div>

      <div ref={emptyRef} className="scroll-mt-4">
        {secondary === "archive" && isAdmin && (
          <div className="mt-3">
            <ImportArchiveCard />
          </div>
        )}
        {secondary === "empty" && (
          <form
            onSubmit={onCreateEmpty}
            className="mt-3 rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
          >
            <div className="space-y-5 p-4 sm:p-5">
              <FormField
                label="Project name"
                hint="Lowercase letters, digits and dashes. Used in URLs."
                error={nameError ?? (name ? projectNameProblem(name.trim()) : null)}
                htmlFor="project-name"
              >
                <Input
                  id="project-name"
                  ref={nameInputRef}
                  name="name"
                  value={name}
                  onChange={(e) => {
                    setName(e.target.value);
                    setNameError(null);
                  }}
                  placeholder="e.g. my-product"
                  aria-invalid={nameError ? true : undefined}
                  spellCheck={false}
                  autoComplete="off"
                  className={monoInputClass}
                  autoFocus
                />
              </FormField>
              <FormField label="Description" hint="Shown on the projects list." optional htmlFor="project-description">
                <Input
                  id="project-description"
                  name="description"
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                  placeholder="e.g. Storefront and its API"
                  className={inputClass}
                  maxLength={120}
                />
              </FormField>
              <AdvancedFields
                {...advancedProps}
                repo={{
                  repoURL,
                  setRepoURL,
                  repoBranch,
                  setRepoBranch,
                  installationId,
                  setInstallationId,
                  installations: (installations.data ?? []).map((i) => ({
                    id: i.id,
                    label: `${i.accountLogin} (${i.accountType.toLowerCase()})`,
                  })),
                }}
              />
              {formError && (
                <p role="alert" className="text-xs text-[var(--error)]">
                  {formError}
                </p>
              )}
            </div>
            <footer className="flex flex-col-reverse gap-3 border-t border-[var(--border-subtle)] px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:px-5">
              <Link href="/projects" className="text-center text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)]">
                Cancel
              </Link>
              <Button type="submit" disabled={submitting} className="h-9 px-4">
                {submitting ? "Creating…" : "Create empty project"}
                <ArrowRight className="h-4 w-4" />
              </Button>
            </footer>
          </form>
        )}
      </div>
    </div>
  );
}

function OptionCard({
  href,
  onClick,
  icon,
  title,
  hint,
  active,
}: {
  href?: string;
  onClick?: () => void;
  icon: React.ReactNode;
  title: string;
  hint: string;
  active?: boolean;
}) {
  const cls =
    "group flex h-full flex-col gap-1.5 rounded-md border px-3 py-3 text-left transition-colors " +
    (active
      ? "border-[var(--accent)] bg-[var(--accent-subtle)]"
      : "border-[var(--border-subtle)] bg-[var(--bg-secondary)] hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]");
  const inner = (
    <>
      <span className="flex items-center justify-between text-[var(--accent)]">
        {icon}
        {/* One glyph for every card: a chevron that nudges right for a
            link and turns down when an inline option is open. */}
        <ChevronRight
          aria-hidden
          className={
            "h-4 w-4 text-[var(--text-tertiary)] transition-transform " +
            (active ? "rotate-90" : "motion-safe:group-hover:translate-x-0.5")
          }
        />
      </span>
      <span className="text-[13px] font-medium text-[var(--text-primary)]">{title}</span>
      <span className="text-xs leading-snug text-[var(--text-secondary)]">{hint}</span>
    </>
  );
  if (href) {
    return (
      <Link href={href} className={cls}>
        {inner}
      </Link>
    );
  }
  return (
    <button type="button" onClick={onClick} aria-expanded={active} className={cls}>
      {inner}
    </button>
  );
}

function Disclosure({
  open,
  onToggle,
  title,
  summary,
  invalid,
  children,
}: {
  open: boolean;
  onToggle: () => void;
  title: string;
  summary?: string;
  invalid?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-left"
      >
        <ChevronRight
          className={"h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)] transition-transform " + (open ? "rotate-90" : "")}
        />
        <span className="text-[13px] font-medium">{title}</span>
        {summary && !open && (
          <span
            className={
              "ml-auto min-w-0 truncate text-xs " + (invalid ? "text-[var(--error)]" : "text-[var(--text-tertiary)]")
            }
          >
            {summary}
          </span>
        )}
      </button>
      {open && <div className="border-t border-[var(--border-subtle)] p-3 sm:p-4">{children}</div>}
    </div>
  );
}

function AdvancedFields({
  open,
  onToggle,
  projectName,
  serviceName,
  instanceDomain,
  baseDomain,
  setBaseDomain,
  namespace,
  setNamespace,
  previewsEnabled,
  setPreviewsEnabled,
  previewsTTLDays,
  setPreviewsTTLDays,
  repo,
}: {
  open: boolean;
  onToggle: () => void;
  projectName: string;
  serviceName: string;
  instanceDomain: string;
  baseDomain: string;
  setBaseDomain: (v: string) => void;
  namespace: string;
  setNamespace: (v: string) => void;
  previewsEnabled: boolean;
  setPreviewsEnabled: (v: boolean) => void;
  previewsTTLDays: string;
  setPreviewsTTLDays: (v: string) => void;
  // Only the empty-project form sets a default repo by hand; the import
  // path takes it from the picked repo.
  repo?: {
    repoURL: string;
    setRepoURL: (v: string) => void;
    repoBranch: string;
    setRepoBranch: (v: string) => void;
    installationId: number;
    setInstallationId: (v: number) => void;
    installations: { id: number; label: string }[];
  };
}) {
  const previewProject = projectName || "my-product";
  const exampleHost = defaultServiceHost(serviceName, previewProject, baseDomain, instanceDomain);
  const sameNameHost = defaultServiceHost(previewProject, previewProject, baseDomain, instanceDomain);
  const summary = [
    baseDomain.trim() || "default domain",
    previewsEnabled ? "PR previews on" : "previews off",
    namespace.trim() ? `namespace ${namespace.trim()}` : null,
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <Disclosure open={open} onToggle={onToggle} title="Advanced" summary={summary}>
      <div className="space-y-5">
        <FormField
          label="Base domain"
          hint="Services get hosts under it. Leave empty to use this kuso instance's domain."
          optional
          htmlFor="project-base-domain"
        >
          <Input
            id="project-base-domain"
            name="baseDomain"
            value={baseDomain}
            onChange={(e) => setBaseDomain(e.target.value)}
            placeholder="e.g. shop.example.com"
            spellCheck={false}
            className={monoInputClass}
          />
          <div className="mt-2 space-y-1 text-xs text-[var(--text-secondary)]">
            <p className="flex items-center gap-1.5">
              <Globe className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" />
              <span className="min-w-0 truncate">
                A service named <span className="font-mono">{serviceName}</span> is served at{" "}
                <span className="font-mono text-[var(--text-primary)]">https://{exampleHost}</span>
              </span>
            </p>
            {baseDomain.trim() && (
              <p>
                Point a wildcard DNS record{" "}
                <span className="font-mono">*.{baseDomain.trim().replace(/^\.+|\.+$/g, "")}</span> (and the bare
                domain) at the cluster.
              </p>
            )}
            <HostClashHint hosts={[exampleHost, sameNameHost]} />
          </div>
        </FormField>

        {repo && (
          <>
            <FormField
              label="Default repository"
              hint="Services added without their own repo build from this one."
              optional
              htmlFor="project-repo"
            >
              <div className="flex flex-col gap-2 sm:flex-row">
                <Input
                  id="project-repo"
                  value={repo.repoURL}
                  onChange={(e) => repo.setRepoURL(e.target.value)}
                  placeholder="e.g. https://github.com/acme/shop"
                  spellCheck={false}
                  className={monoInputClass + " sm:flex-1"}
                />
                <Input
                  value={repo.repoBranch}
                  onChange={(e) => repo.setRepoBranch(e.target.value)}
                  placeholder="branch, e.g. main"
                  aria-label="Default branch"
                  spellCheck={false}
                  className={monoInputClass + " sm:w-40"}
                />
              </div>
            </FormField>
            {repo.installations.length > 0 && (
              <FormField
                label="GitHub App installation"
                hint="Used to clone private repositories and receive pushes."
                htmlFor="project-installation"
              >
                <Select
                  id="project-installation"
                  value={repo.installationId}
                  onChange={(e) => repo.setInstallationId(Number(e.target.value))}
                >
                  <option value={0}>None</option>
                  {repo.installations.map((i) => (
                    <option key={i.id} value={i.id}>
                      {i.label}
                    </option>
                  ))}
                </Select>
              </FormField>
            )}
          </>
        )}

        <FormField
          label="Namespace"
          hint="The Kubernetes namespace the project's workloads run in."
          optional
          htmlFor="project-namespace"
        >
          <Input
            id="project-namespace"
            value={namespace}
            onChange={(e) => setNamespace(e.target.value)}
            placeholder={`e.g. kuso-${previewProject}`}
            spellCheck={false}
            className={monoInputClass}
          />
        </FormField>

        <FormField label="Preview environments" hint="Each pull request gets its own deploy and URL.">
          <label className="flex items-center gap-2 text-[13px]">
            <input
              type="checkbox"
              checked={previewsEnabled}
              onChange={(e) => setPreviewsEnabled(e.target.checked)}
              className="h-4 w-4 accent-[var(--accent)]"
            />
            Deploy a preview for every pull request
          </label>
          {previewsEnabled && (
            <label className="flex flex-wrap items-center gap-2 text-xs text-[var(--text-secondary)]">
              Delete previews after
              <Input
                type="number"
                min={1}
                max={30}
                value={previewsTTLDays}
                onChange={(e) => setPreviewsTTLDays(e.target.value)}
                placeholder={String(DEFAULT_PREVIEW_TTL_DAYS)}
                aria-label="Preview TTL in days"
                className="h-8 w-20 font-mono text-[13px]"
              />
              days
            </label>
          )}
        </FormField>
      </div>
    </Disclosure>
  );
}
