"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Github, ArrowRight, Check, Plus, Box } from "lucide-react";
import { toast } from "sonner";
import {
  useInstallURL,
  useInstallations,
  useGithubRepos,
  useDetectRuntime,
  useScanAddons,
  type DetectRuntimeResponse,
  type AddonSuggestion,
} from "@/features/github";
import { useRouteParams } from "@/lib/dynamic-params";
import { addAddon, useAddons, useProject, useServices } from "@/features/projects";
import { Perms, useCan, useSession } from "@/features/auth";
import { useRegistryCredentials } from "@/features/registry-credentials";
import { addonLabel } from "@/components/addon/AddonIcon";
import { triggerBuild } from "@/features/services";
import { api, ApiError } from "@/lib/api-client";
import { serviceShortName } from "@/lib/utils";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { slugifyServiceName } from "@/features/services/slug";

// A repo pick, normalised across the admin (installations) and
// non-admin (flat /api/github/repos) sources.
interface PickableRepo {
  fullName: string;
  defaultBranch: string;
  private?: boolean;
}

type DetectState = "idle" | "detecting" | "done" | "failed";

// Addon kinds a scan suggestion can turn into with one click — the
// scanner also reports kinds kuso has no chart for (kafka, memcached…).
const SUGGESTABLE_ADDON_KINDS = new Set(["postgres", "mysql", "mongodb", "redis", "rabbitmq", "clickhouse"]);

// detectedLabel turns the detector's reason into short UI text,
// e.g. "nixpacks (no Dockerfile)".
function detectedLabel(res: DetectRuntimeResponse): string {
  const why = res.reason === "fallback" ? "no Dockerfile" : res.reason.replace(/ detected$/, "");
  return why ? `${res.runtime} (${why})` : res.runtime;
}

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

// AddServiceView is the per-project add-service flow. Pick a repo,
// kuso detects the runtime + port, you confirm name + path, click
// add. The wizard intentionally only adds ONE service at a time —
// monorepo support stays via repeated runs of this flow rather than
// the legacy multi-row UI which conflated project + services.
export function AddServiceView() {
  const router = useRouter();
  const params = useRouteParams<{ project: string }>(["project"]);
  const project = params.project ?? "";

  const installURL = useInstallURL();
  const session = useSession();
  const isAdmin = useCan(Perms.SettingsAdmin);
  const sessionReady = !!session.data;
  // Admins read installations (carries the private flag); everyone else
  // gets the projects:create-gated flat list.
  const installs = useInstallations({ enabled: sessionReady && isAdmin });
  const flatRepos = useGithubRepos({ enabled: sessionReady && !isAdmin });
  const reposQuery = isAdmin ? installs : flatRepos;
  const reposForbidden = reposQuery.error instanceof ApiError && reposQuery.error.status === 403;
  const detect = useDetectRuntime();
  const scan = useScanAddons();
  const projectAddons = useAddons(project);
  const registryCreds = useRegistryCredentials(project);

  // Source mode: "repo" wires a GitHub repo + kaniko/buildkit build.
  // "image" deploys a pre-built OCI image directly (no build, no
  // GitHub App needed). The image path is the escape hatch for
  // single-tenant teams that publish their images via their own CI
  // (or for evaluating kuso against a public image like
  // `nginx:1.27-alpine`). Selecting it hides the repo picker
  // entirely so a user without a configured GitHub App can still
  // get to first compute.
  const [source, setSource] = useState<"repo" | "image">("repo");
  const [imageRepo, setImageRepo] = useState("");
  const [imageTag, setImageTag] = useState("latest");
  // Registry credential secretName for image.pullSecret; "" = public
  // pull. `null` means "follow the image host" until the user picks.
  const [pullSecretChoice, setPullSecretChoice] = useState<string | null>(null);
  const autoPullSecret = useMemo(() => {
    if (!imageRepo.trim()) return "";
    const host = imageRegistryHost(imageRepo);
    return (registryCreds.data ?? []).find((c) => c.registry === host)?.secretName ?? "";
  }, [imageRepo, registryCreds.data]);
  const pullSecret = pullSecretChoice ?? autoPullSecret;

  const [picked, setPicked] = useState<{ installationId: number; repo: PickableRepo } | null>(null);
  // Display name is the free-form label the user types (e.g. "Todo
  // API"). It's stored as-is on the CR and shown in the canvas /
  // overlay header. The URL slug is auto-derived via slugifyServiceName
  // — visible in the form as a read-only preview. Renaming the slug
  // afterwards is destructive (clones kube resources) and lives behind
  // the Settings → Danger zone path.
  const [name, setName] = useState("");
  const slug = useMemo(() => slugifyServiceName(name), [name]);
  const [path, setPath] = useState("");
  // "" until detection lands (or the user picks one).
  const [runtime, setRuntime] = useState<string>("");
  const [detectState, setDetectState] = useState<DetectState>("idle");
  // Web service vs worker — a worker isn't a build runtime, it reuses a
  // sibling's image with a different command.
  const [kind, setKind] = useState<"web" | "worker">("web");
  const [suggestions, setSuggestions] = useState<AddonSuggestion[]>([]);
  const [addonPicks, setAddonPicks] = useState<Set<string>>(new Set());
  // dockerfile overrides the Dockerfile filename for runtime=dockerfile
  // (relative to the repo path). Empty = "Dockerfile".
  const [dockerfile, setDockerfile] = useState<string>("");
  const [command, setCommand] = useState<string>("");
  // fromService: the sibling service whose built image a
  // runtime=worker service reuses. Required by the server for
  // workers — a worker has no repo/build of its own.
  const [fromService, setFromService] = useState<string>("");
  const [port, setPort] = useState<string>("");
  const [reason, setReason] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [repoQuery, setRepoQuery] = useState("");

  // Per-field validation errors, shown inline under the field (with
  // aria-invalid on the input) instead of a fire-and-forget toast.
  // Toasts stay reserved for server-side failures. Keys map to the
  // fields this form can reject client-side.
  const [fieldErrors, setFieldErrors] = useState<{
    name?: string;
    image?: string;
    repo?: string;
    runtime?: string;
    fromService?: string;
  }>({});
  const clearFieldError = (key: keyof typeof fieldErrors) =>
    setFieldErrors((prev) => (prev[key] ? { ...prev, [key]: undefined } : prev));
  // Refs so submit can focus the first invalid field. The two name
  // inputs (image mode vs repo mode) are in exclusive branches, so
  // one ref safely serves both.
  const nameInputRef = useRef<HTMLInputElement>(null);
  const imageInputRef = useRef<HTMLInputElement>(null);
  const repoSearchRef = useRef<HTMLInputElement>(null);

  const allRepos = useMemo((): { installationId: number; repo: PickableRepo }[] => {
    if (isAdmin) {
      return (installs.data ?? []).flatMap((inst) =>
        inst.repositories.map((r) => ({ installationId: inst.id, repo: r })),
      );
    }
    return (flatRepos.data ?? []).map((r) => ({
      installationId: r.installationId,
      repo: { fullName: r.fullName, defaultBranch: r.defaultBranch },
    }));
  }, [isAdmin, installs.data, flatRepos.data]);

  const filteredRepos = useMemo(() => {
    const q = repoQuery.trim().toLowerCase();
    if (!q) return allRepos;
    return allRepos.filter(({ repo }) => repo.fullName.toLowerCase().includes(q));
  }, [allRepos, repoQuery]);

  // Candidate image sources for runtime=worker: the project's existing
  // services, minus workers (their image comes from someone else) and
  // image services (no builds, so their tag never propagates to
  // consumers). Short names — that's what spec.fromService stores.
  const projectServices = useServices(project);
  const workerSources = useMemo(
    () =>
      (projectServices.data ?? [])
        .filter((s) => s.spec.runtime !== "worker" && s.spec.runtime !== "image")
        .map((s) => serviceShortName(project, s.metadata.name)),
    [projectServices.data, project]
  );

  // Monotonic token for runtime detection. Detect requests aren't
  // cancellable, so a user who picks repo A, changes their mind, and
  // picks repo B can have A's (slower) response land AFTER B's —
  // stamping A's runtime+port onto B's form. Latest-wins: only the
  // response matching the most recent pick is applied.
  const detectSeq = useRef(0);

  // Preselect the project's default repository (if the GitHub App can
  // see it) once, so "Add service" on a repo-backed project starts one
  // click shorter. A ref guard keeps "change" from re-picking it.
  const projectQuery = useProject(project);
  const defaultRepoURL = projectQuery.data?.project?.spec?.defaultRepo?.url ?? "";
  const defaultRepoApplied = useRef(false);
  useEffect(() => {
    if (defaultRepoApplied.current || picked || !defaultRepoURL || allRepos.length === 0) return;
    defaultRepoApplied.current = true;
    const full = defaultRepoURL
      .replace(/^https?:\/\/github\.com\//, "")
      .replace(/\.git$/, "")
      .replace(/\/+$/, "")
      .toLowerCase();
    const match = allRepos.find(({ repo }) => repo.fullName.toLowerCase() === full);
    if (match) setPicked({ installationId: match.installationId, repo: match.repo });
  }, [defaultRepoURL, allRepos, picked]);

  // Prefill name from repo on first pick.
  useEffect(() => {
    if (!picked) return;
    // Repo name is already kebab-case in 99% of cases, so it doubles
    // as a sensible display-name default — slug derives back to itself.
    const repoName = picked.repo.fullName.split("/")[1] ?? "service";
    if (!name) setName(repoName);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [picked]);

  // Detect runtime + port for the picked repo, and again when the
  // monorepo path changes (debounced so typing "apps/api" doesn't fire
  // one request per keystroke).
  useEffect(() => {
    if (!picked) return;
    const [owner, repoOnly] = picked.repo.fullName.split("/");
    const seq = ++detectSeq.current;
    setDetectState("detecting");
    setReason(null);
    const timer = setTimeout(
      () => {
        const target = {
          installationId: picked.installationId,
          owner: owner ?? "",
          repo: repoOnly ?? "",
          branch: picked.repo.defaultBranch,
          path: path.trim(),
        };
        detect
          .mutateAsync(target)
          .then((res: DetectRuntimeResponse) => {
            if (seq !== detectSeq.current) return; // stale — a newer pick/path superseded this
            setRuntime(res.runtime);
            clearFieldError("runtime");
            if (res.port) setPort(String(res.port));
            setReason(detectedLabel(res));
            setDetectState("done");
          })
          .catch(() => {
            if (seq !== detectSeq.current) return;
            setDetectState("failed");
          });
        scan
          .mutateAsync(target)
          .then((res) => {
            if (seq !== detectSeq.current) return;
            setSuggestions(res.suggestions ?? []);
          })
          .catch(() => {
            if (seq === detectSeq.current) setSuggestions([]);
          });
      },
      path.trim() ? 500 : 0,
    );
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [picked, path]);

  const addonSuggestions = useMemo(() => {
    const have = new Set((projectAddons.data ?? []).map((a) => a.spec.kind));
    return suggestions.filter((s) => SUGGESTABLE_ADDON_KINDS.has(s.kind) && !have.has(s.kind));
  }, [suggestions, projectAddons.data]);

  const toggleAddonPick = (k: string) =>
    setAddonPicks((prev) => {
      const next = new Set(prev);
      if (next.has(k)) next.delete(k);
      else next.add(k);
      return next;
    });

  const effectiveRuntime = kind === "worker" ? "worker" : runtime;

  const onAdd = async () => {
    // Client-side validation → inline per-field errors + focus the
    // first invalid field (in visual order). No toasts here — those
    // auto-dismiss and never mark the field, which is how users ended
    // up staring at a form that "does nothing".
    const errs: typeof fieldErrors = {};
    if (!name.trim()) {
      errs.name = "Service name is required.";
    } else if (!slug) {
      errs.name = "Name needs at least one letter or digit.";
    }
    if (source === "repo" && !picked) {
      errs.repo = "Pick a repository to continue.";
    }
    if (source === "image" && !imageRepo.trim()) {
      errs.image = "Image repository is required — e.g. ghcr.io/owner/app.";
    }
    if (source === "repo" && kind === "worker" && !fromService) {
      errs.fromService = "Pick the service whose image this worker runs.";
    }
    if (source === "repo" && picked && kind === "web" && !runtime) {
      errs.runtime = "Pick a runtime.";
    }
    setFieldErrors(errs);
    if (Object.keys(errs).some((k) => errs[k as keyof typeof errs])) {
      // Focus follows visual order: repo picker sits above the name
      // field in repo mode; in image mode name comes before image.
      if (errs.repo) repoSearchRef.current?.focus();
      else if (errs.name) nameInputRef.current?.focus();
      else if (errs.image) imageInputRef.current?.focus();
      // fromService is a button group — its inline error is announced
      // via role="alert"; there's no text input to focus.
      return;
    }
    setSubmitting(true);
    try {
      // Body shape diverges by source. Server validates either way:
      //   - source=repo  → runtime in {dockerfile,nixpacks,static,
      //     buildpacks,worker}; repo.{url,defaultBranch,path}; github
      //     installationId for private repos.
      //   - source=image → runtime="image"; image.{repository,tag};
      //     no repo + no github.
      let body: Record<string, unknown>;
      if (source === "image") {
        body = {
          name: slug,
          displayName: name.trim(),
          runtime: "image",
          image: {
            repository: imageRepo.trim(),
            tag: imageTag.trim() || "latest",
            ...(pullSecret ? { pullSecret } : {}),
          },
          ...(port ? { port: parseInt(port, 10) } : {}),
        };
      } else {
        body = {
          name: slug,
          displayName: name.trim(),
          repo: {
            url: `https://github.com/${picked!.repo.fullName}`,
            defaultBranch: picked!.repo.defaultBranch,
            ...(path.trim() ? { path: path.trim() } : {}),
          },
          runtime: effectiveRuntime,
          ...(effectiveRuntime === "dockerfile" && dockerfile.trim()
            ? { dockerfile: dockerfile.trim() }
            : {}),
          ...(effectiveRuntime === "worker" && command.trim()
            ? { command: command.trim().split(/\s+/).filter(Boolean) }
            : {}),
          // fromService is REQUIRED server-side for runtime=worker
          // (the worker reuses this sibling's built image). Validated
          // above so we never submit a worker without it.
          ...(effectiveRuntime === "worker" ? { fromService } : {}),
          ...(port && effectiveRuntime !== "worker" ? { port: parseInt(port, 10) } : {}),
          github: { installationId: picked!.installationId },
        };
      }
      // Create picked addons before the service: a service with no
      // explicit subscription list mounts every project addon, so the
      // new service boots with DATABASE_URL etc. already set.
      if (source === "repo") {
        for (const addonKind of addonPicks) {
          if (!addonSuggestions.some((s) => s.kind === addonKind)) continue;
          try {
            await addAddon(project, { name: addonKind, kind: addonKind });
          } catch (ae) {
            if (!(ae instanceof ApiError && ae.status === 409)) throw ae;
          }
        }
      }
      // Land on the new service's Deployments tab so the first build is
      // the first thing the user sees, not the bare canvas.
      const serviceHref = `/projects/${encodeURIComponent(project)}?service=${encodeURIComponent(slug)}&tab=deployments`;
      const created = await api(`/api/projects/${encodeURIComponent(project)}/services`, {
        method: "POST",
        body,
      });

      // The server may report the outcome of the first build it
      // triggered on our behalf via an optional `firstBuild` field on
      // the 201. Typed loosely + guarded so this is a no-op on servers
      // that don't send it yet (older kuso versions).
      const firstBuild =
        created && typeof created === "object"
          ? ((created as Record<string, unknown>).firstBuild as
              | { triggered?: boolean; error?: string }
              | undefined)
          : undefined;
      if (firstBuild?.error) {
        toast.warning(
          `Service created, but the first build failed to start: ${firstBuild.error} — trigger a build from the service page.`,
          { duration: Infinity, closeButton: true },
        );
        router.replace(serviceHref);
        return;
      }
      if (firstBuild?.triggered) {
        // Server already kicked the build — don't double-trigger below.
        toast.success(
          source === "repo" && picked
            ? `Service ${name} added — building from ${picked.repo.defaultBranch}`
            : `Service ${name} added — first build started`,
        );
        router.replace(serviceHref);
        return;
      }

      // Kick the first build. AddService creates the CR + production env
      // but does NOT build — without this the service sits at 0/0 until
      // the next git push, while onboarding promises kuso will "start a
      // build immediately". That was the single worst first-run bug:
      // a new user finishes the wizard and watches a service that never
      // deploys.
      //
      // Only for services that actually build from a repo:
      //   - runtime=image deploys a pre-built tag; there is nothing to build.
      //   - runtime=worker reuses its fromService sibling's image and has
      //     no repo of its own (builds.Create rejects it outright).
      //
      // Best-effort: the service IS created at this point, so a failed
      // trigger must not read as a failed creation. We surface it as a
      // warning with the manual next step instead of throwing.
      let buildStarted = false;
      if (source === "repo" && kind !== "worker") {
        try {
          await triggerBuild(project, slug, { branch: picked!.repo.defaultBranch });
          buildStarted = true;
        } catch (be) {
          toast.warning(
            be instanceof ApiError
              ? `Service created, but the first build didn't start: ${be.message}`
              : "Service created, but the first build didn't start — trigger one from the service page.",
          );
        }
      }
      if (buildStarted) {
        toast.success(`Service ${name} added — building from ${picked!.repo.defaultBranch}`);
      } else if (source === "repo" && kind !== "worker") {
        // A warning toast already explained why; don't double-report.
      } else {
        toast.success(`Service ${name} added`);
      }
      router.replace(serviceHref);
    } catch (e) {
      if (e instanceof ApiError) {
        toast.error(e.message);
      } else {
        toast.error(e instanceof Error ? e.message : "Failed to add service");
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="mx-auto max-w-2xl p-6 lg:p-8 space-y-4">
      <header>
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Add service</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          Adding to <span className="font-mono text-[var(--text-primary)]">{project}</span>.{" "}
          {source === "repo"
            ? "Pick a GitHub repo; kuso detects the runtime and port."
            : "Deploy a pre-built OCI image. No build, no GitHub App needed."}
        </p>
      </header>

      {/* Source mode — repo (kaniko/buildkit) or pre-built image.
          Selecting image hides the GitHub-flavoured panels entirely so
          a kuso install without a configured GitHub App can still
          reach first compute. */}
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <div className="border-b border-[var(--border-subtle)] px-4 py-2.5">
          <h2 className="text-sm font-semibold tracking-tight">Source</h2>
        </div>
        <div className="flex gap-2 px-4 py-3">
          <button
            type="button"
            aria-pressed={source === "repo"}
            onClick={() => {
              setSource("repo");
              setFieldErrors({});
            }}
            className={
              "flex flex-1 flex-col gap-1 rounded-md border px-3 py-2 text-left transition-colors " +
              (source === "repo"
                ? "border-[var(--accent)] bg-[var(--accent-subtle)]"
                : "border-[var(--border-subtle)] bg-[var(--bg-primary)] hover:bg-[var(--bg-tertiary)]")
            }
          >
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <Github className="h-3.5 w-3.5" />
              GitHub repo
            </span>
            <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
              kuso builds on every push
            </span>
          </button>
          <button
            type="button"
            aria-pressed={source === "image"}
            onClick={() => {
              setSource("image");
              setFieldErrors({});
            }}
            className={
              "flex flex-1 flex-col gap-1 rounded-md border px-3 py-2 text-left transition-colors " +
              (source === "image"
                ? "border-[var(--accent)] bg-[var(--accent-subtle)]"
                : "border-[var(--border-subtle)] bg-[var(--bg-primary)] hover:bg-[var(--bg-tertiary)]")
            }
          >
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <Box className="h-3.5 w-3.5" />
              Pre-built image
            </span>
            <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
              your own CI publishes the image
            </span>
          </button>
        </div>
      </section>

      {source === "image" ? (
        <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
          <div className="border-b border-[var(--border-subtle)] px-4 py-2.5">
            <h2 className="text-sm font-semibold tracking-tight">Image</h2>
          </div>
          <div className="space-y-3 px-4 py-3">
            <div className="grid grid-cols-2 gap-3">
              <Field
                label="name"
                hint={slug ? `url slug: ${slug}` : "letters / digits / spaces / hyphens"}
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
                  placeholder="My API"
                  aria-invalid={fieldErrors.name ? true : undefined}
                  className="h-8 text-[12px]"
                />
              </Field>
              <Field label="port" hint="container port; defaults to 8080" htmlFor="svc-port">
                <Input
                  id="svc-port"
                  type="number"
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
                  placeholder="8080"
                  className="h-8 font-mono text-[12px]"
                />
              </Field>
            </div>
            <Field
              label="image"
              hint="full registry path; e.g. ghcr.io/owner/app"
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
                placeholder="ghcr.io/owner/app"
                aria-invalid={fieldErrors.image ? true : undefined}
                className="h-8 font-mono text-[12px]"
              />
            </Field>
            <Field label="tag" hint="immutable tags or digests roll predictably; :latest is mutable" htmlFor="svc-tag">
              <Input
                id="svc-tag"
                value={imageTag}
                onChange={(e) => setImageTag(e.target.value)}
                placeholder="latest"
                className="h-8 font-mono text-[12px]"
              />
            </Field>
            <Field
              label="registry login"
              hint={
                (registryCreds.data ?? []).length === 0
                  ? "none saved — public images only"
                  : pullSecretChoice === null && autoPullSecret
                    ? `matched ${imageRegistryHost(imageRepo)}`
                    : undefined
              }
              htmlFor="svc-pull-secret"
            >
              <select
                id="svc-pull-secret"
                value={pullSecret}
                onChange={(e) => setPullSecretChoice(e.target.value)}
                disabled={(registryCreds.data ?? []).length === 0}
                className="block h-8 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px]"
              >
                <option value="">none (public image)</option>
                {(registryCreds.data ?? []).map((c) => (
                  <option key={c.secretName} value={c.secretName}>
                    {c.registry} · {c.username}
                  </option>
                ))}
              </select>
            </Field>
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
              Pulled as-is, no build. Push a new tag from your CI, then bump it here.
            </p>
          </div>
          <footer className="flex items-center justify-between border-t border-[var(--border-subtle)] px-4 py-3">
            <Link
              href={`/projects/${encodeURIComponent(project)}`}
              className="font-mono text-[10px] text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
            >
              ← cancel
            </Link>
            <Button type="button" size="sm" onClick={onAdd} disabled={submitting}>
              <Plus className="h-3.5 w-3.5" />
              {submitting ? "Adding…" : "Add service"}
            </Button>
          </footer>
        </section>
      ) : (
      <>

      {/* Repo picker */}
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <div className="border-b border-[var(--border-subtle)] px-4 py-2.5">
          <h2 className="text-sm font-semibold tracking-tight">Repository</h2>
        </div>
        <div className="px-4 py-3">
          {!sessionReady || installURL.isPending || reposQuery.isLoading ? (
            <Skeleton className="h-24 w-full" />
          ) : !installURL.data?.configured ? (
            isAdmin ? (
              <div className="space-y-2">
                <p className="text-sm text-[var(--text-secondary)]">
                  GitHub App not configured on this kuso instance.
                </p>
                <a
                  href="/settings/github"
                  className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-3 text-xs font-medium hover:bg-[var(--accent-subtle)]"
                >
                  <Github className="h-3.5 w-3.5" />
                  Configure GitHub App
                </a>
              </div>
            ) : (
              <p className="text-sm text-[var(--text-secondary)]">
                GitHub isn&apos;t set up yet. Ask an admin to connect the GitHub App, or use a
                pre-built image.
              </p>
            )
          ) : reposForbidden ? (
            <p className="text-sm text-[var(--text-secondary)]">
              You can&apos;t browse repositories. Ask an admin for project-create access, or use a
              pre-built image.
            </p>
          ) : reposQuery.isError ? (
            <p role="alert" className="text-sm text-[var(--error)]">
              Couldn&apos;t load repositories.{" "}
              <button type="button" className="underline" onClick={() => void reposQuery.refetch()}>
                retry
              </button>
            </p>
          ) : allRepos.length === 0 && !isAdmin ? (
            <p className="text-sm text-[var(--text-secondary)]">
              No repositories connected. Ask an admin to install the GitHub App.
            </p>
          ) : allRepos.length === 0 ? (
            <div className="space-y-2">
              <p className="text-sm text-[var(--text-secondary)]">
                No GitHub installations yet.
              </p>
              <a
                href={installURL.data.url}
                target="_blank"
                rel="noreferrer"
                className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-3 text-xs font-medium hover:bg-[var(--accent-subtle)]"
              >
                <Github className="h-3.5 w-3.5" />
                Install kuso GitHub App
              </a>
            </div>
          ) : !picked ? (
            <div className="space-y-2">
              {fieldErrors.repo && (
                <p role="alert" className="text-[11px] text-[var(--error)]">
                  {fieldErrors.repo}
                </p>
              )}
              <Input
                ref={repoSearchRef}
                type="search"
                value={repoQuery}
                onChange={(e) => setRepoQuery(e.target.value)}
                placeholder={`Filter ${allRepos.length} repositories…`}
                aria-invalid={fieldErrors.repo ? true : undefined}
                className="h-8 font-mono text-[12px]"
                autoFocus
              />
              <ul className="max-h-72 overflow-auto divide-y divide-[var(--border-subtle)] rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]">
                {filteredRepos.length === 0 ? (
                  <li className="px-3 py-2 text-xs text-[var(--text-tertiary)]">
                    No repos match{repoQuery ? ` "${repoQuery}"` : ""}.
                  </li>
                ) : (
                  filteredRepos.map(({ installationId, repo }) => (
                    <li key={`${installationId}/${repo.fullName}`}>
                      <button
                        type="button"
                        onClick={() => {
                          setPicked({ installationId, repo });
                          clearFieldError("repo");
                        }}
                        className="flex w-full items-center justify-between gap-3 px-3 py-1.5 text-left text-[12px] hover:bg-[var(--bg-tertiary)]"
                      >
                        <span className="flex items-center gap-2 truncate">
                          <Github className="h-3 w-3 text-[var(--text-tertiary)]" />
                          <span className="font-mono truncate">{repo.fullName}</span>
                          {repo.private && (
                            <span className="rounded bg-[var(--bg-tertiary)] px-1 py-0.5 font-mono text-[9px] text-[var(--text-tertiary)]">
                              private
                            </span>
                          )}
                        </span>
                        <ArrowRight className="h-3 w-3 text-[var(--text-tertiary)] shrink-0" />
                      </button>
                    </li>
                  ))
                )}
              </ul>
            </div>
          ) : (
            <div className="flex items-center justify-between rounded-md border border-[var(--accent)]/40 bg-[var(--accent-subtle)] px-3 py-2 text-[12px]">
              <span className="flex items-center gap-2 truncate">
                <Check className="h-3.5 w-3.5 text-[var(--accent)]" />
                <span className="font-mono truncate">{picked.repo.fullName}</span>
                <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
                  {picked.repo.defaultBranch}
                </span>
              </span>
              <button
                type="button"
                onClick={() => {
                  // Invalidate any in-flight detection so a late
                  // response can't stamp the cleared form.
                  detectSeq.current++;
                  setPicked(null);
                  setName("");
                  setPath("");
                  setReason(null);
                  setRuntime("");
                  setDetectState("idle");
                  setSuggestions([]);
                  setAddonPicks(new Set());
                }}
                className="font-mono text-[10px] text-[var(--text-secondary)] underline"
              >
                change
              </button>
            </div>
          )}
        </div>
      </section>

      {picked && (
        <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
          <div className="border-b border-[var(--border-subtle)] px-4 py-2.5">
            <h2 className="flex items-center gap-2 text-sm font-semibold tracking-tight">
              <RuntimeIcon runtime={effectiveRuntime || "dockerfile"} />
              Service
            </h2>
          </div>
          <div className="space-y-3 px-4 py-3">
            <div className="grid grid-cols-2 gap-3">
              <Field
                label="name"
                hint={
                  slug
                    ? `url slug: ${slug}`
                    : "letters / digits / spaces / hyphens"
                }
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
                  aria-invalid={fieldErrors.name ? true : undefined}
                  className="h-8 text-[12px]"
                />
              </Field>
              {kind === "web" && (
                <Field label="port" hint="container port" htmlFor="svc-port">
                  <Input
                    id="svc-port"
                    type="number"
                    value={port}
                    onChange={(e) => setPort(e.target.value)}
                    placeholder="auto"
                    className="h-8 font-mono text-[12px]"
                  />
                </Field>
              )}
            </div>
            <Field label="path" hint="monorepo subdir; root if empty" htmlFor="svc-path">
              <Input
                id="svc-path"
                value={path}
                onChange={(e) => setPath(e.target.value)}
                placeholder="apps/api"
                className="h-8 font-mono text-[12px]"
              />
            </Field>
            <Field label="type">
              <div className="inline-flex flex-wrap gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
                {([
                  ["web", "web service"],
                  ["worker", "worker"],
                ] as const).map(([k, label]) => (
                  <button
                    key={k}
                    type="button"
                    aria-pressed={kind === k}
                    onClick={() => {
                      setKind(k);
                      clearFieldError("fromService");
                      clearFieldError("runtime");
                    }}
                    className={
                      "rounded px-2 py-1 font-mono text-[11px] " +
                      (kind === k
                        ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                        : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]")
                    }
                  >
                    {label}
                  </button>
                ))}
              </div>
            </Field>
            {kind === "web" && (
              <Field label="runtime" error={fieldErrors.runtime}>
                <div className="inline-flex flex-wrap gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
                  {["dockerfile", "nixpacks", "static", "buildpacks"].map((r) => (
                    <button
                      key={r}
                      type="button"
                      aria-pressed={runtime === r}
                      onClick={() => {
                        setRuntime(r);
                        clearFieldError("runtime");
                      }}
                      className={
                        "rounded px-2 py-1 font-mono text-[11px] " +
                        (runtime === r
                          ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                          : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]")
                      }
                    >
                      {r}
                    </button>
                  ))}
                </div>
                {detectState === "detecting" ? (
                  <p className="font-mono text-[10px] text-[var(--text-tertiary)]">Detecting…</p>
                ) : detectState === "failed" ? (
                  <p role="alert" className="font-mono text-[10px] text-[var(--error)]">
                    Couldn&apos;t detect the runtime — pick one.
                  </p>
                ) : reason ? (
                  <p className="font-mono text-[10px] text-[var(--text-tertiary)]">detected: {reason}</p>
                ) : null}
              </Field>
            )}
            {kind === "web" && runtime === "dockerfile" && (
              <Field label="dockerfile" hint="path to Dockerfile; default if empty" htmlFor="svc-dockerfile">
                <Input
                  id="svc-dockerfile"
                  value={dockerfile}
                  onChange={(e) => setDockerfile(e.target.value)}
                  placeholder="Dockerfile"
                  className="h-8 font-mono text-[12px]"
                />
                <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
                  Relative to the path above. Override for a non-standard name or a monorepo
                  build file, e.g. <span className="text-[var(--text-secondary)]">docker/Dockerfile.prod</span>.
                </p>
              </Field>
            )}
            {kind === "worker" && (
              <Field
                label="runs image of"
                hint="sibling service whose built image this worker reuses"
                error={fieldErrors.fromService}
              >
                {workerSources.length === 0 ? (
                  <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
                    No buildable services in this project yet — add a web service first,
                    then come back for its worker.
                  </p>
                ) : (
                  <div className="inline-flex flex-wrap gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
                    {workerSources.map((s) => (
                      <button
                        key={s}
                        type="button"
                        aria-pressed={fromService === s}
                        onClick={() => {
                          setFromService(s);
                          clearFieldError("fromService");
                        }}
                        className={
                          "rounded px-2 py-1 font-mono text-[11px] " +
                          (fromService === s
                            ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                            : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]")
                        }
                      >
                        {s}
                      </button>
                    ))}
                  </div>
                )}
              </Field>
            )}
            {kind === "worker" && (
              <Field label="command" htmlFor="svc-command">
                <Input
                  id="svc-command"
                  value={command}
                  onChange={(e) => setCommand(e.target.value)}
                  placeholder="bundle exec sidekiq    OR    sh -c &quot;celery worker -A app&quot;"
                  className="h-8 font-mono text-[12px]"
                />
                <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
                  Workers run the same image as a sibling web service but with this command.
                  No HTTP port, no ingress, no health probes.
                </p>
              </Field>
            )}
            {addonSuggestions.length > 0 && (
              <Field label="needs">
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
                          "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 font-mono text-[11px] " +
                          (on
                            ? "border-[var(--accent)] bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                            : "border-[var(--border-subtle)] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]")
                        }
                      >
                        {on ? <Check className="h-3 w-3" /> : <Plus className="h-3 w-3" />}
                        Add {addonLabel(sug.kind)}
                      </button>
                    );
                  })}
                </div>
              </Field>
            )}
          </div>
          <footer className="flex items-center justify-between border-t border-[var(--border-subtle)] px-4 py-3">
            <Link
              href={`/projects/${encodeURIComponent(project)}`}
              className="font-mono text-[10px] text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
            >
              ← cancel
            </Link>
            <Button
              type="button"
              size="sm"
              onClick={onAdd}
              // Workers can't submit without a source service — the
              // server hard-rejects runtime=worker with no fromService.
              disabled={
                submitting ||
                (kind === "web" && detectState === "detecting") ||
                (kind === "worker" && !fromService)
              }
            >
              <Plus className="h-3.5 w-3.5" />
              {submitting ? "Adding…" : "Add service"}
            </Button>
          </footer>
        </section>
      )}
      </>
      )}
    </div>
  );
}

function Field({
  label,
  hint,
  error,
  htmlFor,
  children,
}: {
  label: string;
  hint?: string;
  error?: string;
  // Set when the field wraps a single input; button groups have no
  // single control to point a <label> at.
  htmlFor?: string;
  children: React.ReactNode;
}) {
  const labelClass = "block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]";
  return (
    <div className="space-y-1">
      {htmlFor ? (
        <label htmlFor={htmlFor} className={labelClass}>
          {label}
        </label>
      ) : (
        <div className={labelClass}>{label}</div>
      )}
      {children}
      {error ? (
        <div role="alert" className="text-[11px] text-[var(--error)]">
          {error}
        </div>
      ) : (
        hint && <div className="text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>
      )}
    </div>
  );
}
